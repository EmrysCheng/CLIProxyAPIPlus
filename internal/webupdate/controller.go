// Package webupdate implements opt-in Portable updates; it never compiles upstreams.
package webupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"cliproxyapi-plus/internal/product"
	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
)

const Protocol = 1
const maxArchive = int64(512 << 20)
const maxBinary = int64(384 << 20)
const maxSnapshot = int64(16 << 30)

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Options struct {
	DisabledReason string   `json:"disabled_reason,omitempty"`
	Enabled        bool     `json:"enabled"`
	Binary         string   `json:"binary"`
	Config         string   `json:"config"`
	Data           string   `json:"data"`
	Auth           string   `json:"auth"`
	TLSCert        string   `json:"tls_cert,omitempty"`
	TLSKey         string   `json:"tls_key,omitempty"`
	HealthURL      string   `json:"health_url"`
	Version        string   `json:"version"`
	WorkDir        string   `json:"work_dir"`
	Args           []string `json:"args"`
}

func (o Options) stateDir() string              { return o.Data + ".updates" }
func (o Options) operationDir(id string) string { return filepath.Join(o.stateDir(), id) }

type Operation struct {
	ID                string         `json:"id"`
	Kind              string         `json:"kind"`
	State             string         `json:"state"`
	FromVersion       string         `json:"from_version"`
	TargetVersion     string         `json:"target_version"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	Error             string         `json:"error,omitempty"`
	FailureCode       string         `json:"failure_code,omitempty"`
	Progress          *BuildProgress `json:"progress,omitempty"`
	RollbackAvailable bool           `json:"rollback_available"`
	// Private transaction fields are never returned by the HTTP handler.
	Options             Options                 `json:"installation"`
	ParentPID           int                     `json:"parent_pid"`
	OriginalHash        string                  `json:"original_hash"`
	TargetHash          string                  `json:"target_hash,omitempty"`
	TargetSourceDigest  string                  `json:"target_source_sha256,omitempty"`
	SourceRequest       *SourceRequest          `json:"source_request,omitempty"`
	SourceWorkspace     string                  `json:"source_workspace,omitempty"`
	SourceCoreCommit    string                  `json:"source_core_commit,omitempty"`
	SourceManagerCommit string                  `json:"source_manager_commit,omitempty"`
	FromUpstreams       map[string]SourceTarget `json:"from_upstreams,omitempty"`
	TargetUpstreams     map[string]SourceTarget `json:"target_upstreams,omitempty"`
	TargetArgs          []string                `json:"target_args,omitempty"`
	LaunchHash          string                  `json:"launch_hash,omitempty"`
	Reference           string                  `json:"reference,omitempty"`
	Snapshot            map[string]string       `json:"snapshot,omitempty"`
	FailedData          string                  `json:"failed_data,omitempty"`
	Retained            []string                `json:"retained,omitempty"`
	FailedConfig        string                  `json:"failed_config,omitempty"`
}

func publicOperation(op Operation) map[string]any {
	code := safeSourceCode(op.FailureCode)
	if code == "" {
		code = publicSourceCode(op)
	}
	message := ""
	if code != "" {
		message = sourceFailureMessages[code]
	} else if op.Error != "" {
		message = "Maintenance did not complete normally. Review the private transaction log for operation " + op.ID + "."
	}
	return map[string]any{"failure_code": code, "progress": publicBuildProgress(op.Progress), "id": op.ID, "kind": op.Kind, "state": op.State, "from_version": op.FromVersion, "target_version": op.TargetVersion, "created_at": op.CreatedAt, "updated_at": op.UpdatedAt, "error": message, "rollback_available": op.RollbackAvailable, "from_upstreams": op.FromUpstreams, "target_upstreams": op.TargetUpstreams}
}
func terminal(s string) bool {
	return s == "succeeded" || s == "rolled_back" || s == "failed" || s == "recovery_required"
}
func newID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func saveOperation(op *Operation) error {
	op.UpdatedAt = stamp()
	return writeJSON(filepath.Join(op.Options.operationDir(op.ID), "operation.json"), op)
}
func readOperation(o Options, id string) (Operation, error) {
	var op Operation
	if !idPattern.MatchString(id) {
		return op, errors.New("invalid operation id")
	}
	path := filepath.Join(o.operationDir(id), "operation.json")
	if err := trustedOwner(o.operationDir(id)); err != nil {
		return op, err
	}
	if err := readJSON(path, &op); err != nil {
		return op, err
	}
	if op.ID != id || op.Options.Data != o.Data || op.Options.Config != o.Config || (o.Binary != "" && op.Options.Binary != o.Binary) {
		return op, errors.New("operation installation identity mismatch")
	}
	return op, nil
}

type Controller struct {
	opts        Options
	releases    *product.Releases
	stop        func()
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	wg          sync.WaitGroup
	active      string
	lock        io.Closer
	currentHash string
	reason      string
}

func New(ctx context.Context, o Options, r *product.Releases, stop func()) *Controller {
	cctx, cancel := context.WithCancel(ctx)
	c := &Controller{opts: o, releases: r, stop: stop, ctx: cctx, cancel: cancel}
	c.currentHash, _ = fileHash(o.Binary)
	if !o.Enabled {
		c.reason = "Web installation disabled. Start a standalone Portable instance with --web-updates; do not combine with a container or service auto-restart policy."
	} else if o.DisabledReason != "" {
		c.reason = o.DisabledReason
	} else if err := validateInstallation(o); err != nil {
		c.reason = err.Error()
	}
	return c
}
func (c *Controller) Close(clean bool) {
	c.cancel()
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != "" && clean {
		_ = writeJSON(filepath.Join(c.opts.operationDir(c.active), "parent-stopped.json"), map[string]any{"pid": os.Getpid()})
	}
	if c.lock != nil {
		_ = c.lock.Close()
		c.lock = nil
	}
}
func (c *Controller) Snapshot() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, _ := os.ReadDir(c.opts.stateDir())
	history := []map[string]any{}
	journals := []Operation{}
	currentHash := c.currentHash
	// Read only private journals. Never expose config paths, restart arguments or secrets.
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		op, err := readOperation(c.opts, entry.Name())
		if err != nil {
			continue
		}
		op.RollbackAvailable = op.Kind == "install" && op.State == "succeeded" && op.TargetHash == currentHash
		journals = append(journals, op)
	}
	sort.SliceStable(journals, func(i, j int) bool { return journals[i].CreatedAt > journals[j].CreatedAt })
	if len(journals) > 50 {
		journals = journals[:50]
	}
	for _, op := range journals {
		history = append(history, publicOperation(op))
	}
	busy := false
	for _, op := range history {
		if !terminal(op["state"].(string)) {
			busy = true
		}
	}
	return map[string]any{"enabled": c.reason == "", "reason": c.reason, "platform": runtime.GOOS + "/" + runtime.GOARCH, "busy": busy, "history": history, "protocol": Protocol}
}
func (c *Controller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	root := "/plus-api/releases/operations"
	if r.URL.Path == root && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(c.Snapshot())
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != root+"/install" && r.URL.Path != root+"/rollback") {
		http.Error(w, `{"error":"not found"}`, 404)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, `{"error":"cross-origin update action rejected"}`, 403)
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		http.Error(w, `{"error":"JSON body required"}`, 415)
		return
	}
	var input struct {
		Version        string `json:"version"`
		SHA256         string `json:"sha256"`
		OperationID    string `json:"operation_id"`
		Confirmation   string `json:"confirmation"`
		AcceptDataLoss bool   `json:"accept_data_loss"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		http.Error(w, `{"error":"invalid update request"}`, 400)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(w, `{"error":"one JSON object required"}`, 400)
		return
	}
	if c.reason != "" {
		http.Error(w, `{"error":"web update capability disabled; see status reason"}`, 409)
		return
	}
	if c.releases.URL == "" && r.URL.Path == root+"/install" {
		http.Error(w, `{"error":"trusted product release index is not configured"}`, 409)
		return
	}
	if err := privateDirectory(c.opts.stateDir()); err != nil {
		http.Error(w, `{"error":"cannot prepare private update directory"}`, 500)
		return
	}
	c.mu.Lock()
	if c.lock != nil {
		c.mu.Unlock()
		http.Error(w, `{"error":"an update is already active"}`, 409)
		return
	}
	lock, err := manager.AcquireDataLock(c.opts.stateDir())
	if err != nil {
		c.mu.Unlock()
		http.Error(w, `{"error":"another update worker is active"}`, 409)
		return
	}
	c.lock = lock
	fail := func(message string) {
		_ = c.lock.Close()
		c.lock = nil
		c.mu.Unlock()
		http.Error(w, fmt.Sprintf(`{"error":%q}`, message), 409)
	}
	if _, err := os.Lstat(c.opts.Data + ".maintenance"); !errors.Is(err, os.ErrNotExist) {
		fail("maintenance or recovery is active")
		return
	}
	if err := c.checkPending(); err != nil {
		fail(err.Error())
		return
	}
	op := Operation{Kind: "install", State: "queued", FromVersion: c.opts.Version, Options: c.opts, ParentPID: os.Getpid(), CreatedAt: stamp()}
	var asset product.Asset
	if strings.HasSuffix(r.URL.Path, "/install") {
		status := c.releases.Check(r.Context())
		if status.Error != "" || status.State != "update_available" || status.Latest == nil {
			fail("no fresh newer verified product release is available")
			return
		}
		if input.Version != status.Latest.Version || input.Confirmation != input.Version {
			fail("release changed or version confirmation is missing")
			return
		}
		asset, err = selectAsset(*status.Latest)
		if err != nil || !strings.EqualFold(input.SHA256, asset.SHA256) {
			fail("no matching current-platform package or release checksum changed")
			return
		}
		op.TargetVersion = input.Version
		op.TargetSourceDigest = status.Latest.SourceDigest
	} else {
		previous, e := readOperation(c.opts, input.OperationID)
		hash, _ := fileHash(c.opts.Binary)
		if e != nil || previous.Kind != "install" || previous.State != "succeeded" || previous.TargetHash != hash {
			fail("rollback requires a successful upgrade matching the current binary")
			return
		}
		if !input.AcceptDataLoss || input.Confirmation != previous.FromVersion {
			fail("confirm the previous version and accept snapshot data restoration")
			return
		}
		if e = verifySnapshot(previous); e != nil {
			fail("rollback snapshot verification failed")
			return
		}
		op.Kind = "rollback"
		op.Reference = previous.ID
		op.TargetVersion = previous.FromVersion
		op.TargetHash = previous.OriginalHash
		op.FromUpstreams = previous.TargetUpstreams
		op.TargetUpstreams = previous.FromUpstreams
		if previous.SourceRequest != nil || len(previous.TargetArgs) > 0 {
			op.TargetArgs = append([]string{}, previous.Options.Args...)
		}
	}
	op.ID, err = newID()
	if err != nil {
		fail("cannot allocate operation")
		return
	}
	op.OriginalHash, err = fileHash(c.opts.Binary)
	if err != nil {
		fail("cannot identify current executable")
		return
	}
	if op.OriginalHash != c.currentHash {
		fail("installed executable changed outside this process; restart before web maintenance")
		return
	}
	if err = privateDirectory(c.opts.operationDir(op.ID)); err == nil {
		err = saveOperation(&op)
	}
	if err != nil {
		fail("cannot persist update transaction")
		return
	}
	c.active = op.ID
	c.wg.Add(1)
	c.mu.Unlock()
	go func() { defer c.wg.Done(); c.prepare(op, asset) }()
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(publicOperation(op))
}
func (c *Controller) checkPending() error {
	entries, err := os.ReadDir(c.opts.stateDir())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && idPattern.MatchString(entry.Name()) {
			op, e := readOperation(c.opts, entry.Name())
			if e != nil {
				return errors.New("unreadable update journal requires offline inspection")
			}
			if !terminal(op.State) || op.State == "recovery_required" {
				return errors.New("unfinished update requires offline recovery")
			}
		}
	}
	return nil
}

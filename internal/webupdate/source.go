package webupdate

import (
	"cliproxyapi-plus/internal/product"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
)

type SourceTarget struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}
type SourceRequest struct {
	Targets map[string]SourceTarget `json:"targets"`
	Current map[string]SourceTarget `json:"current"`
}
type SourceCandidate struct {
	Archive       string `json:"archive"`
	Name          string `json:"name"`
	SHA256        string `json:"sha256"`
	SourceDigest  string `json:"product_source_sha256"`
	Workspace     string `json:"workspace"`
	CoreCommit    string `json:"core_commit"`
	ManagerCommit string `json:"manager_commit"`
	Version       string `json:"version"`
}
type SourceBuilder func(context.Context, SourceRequest, string, func(string)) (SourceCandidate, error)

// StartSource is an internal API called only after the Manager-authorized source
// handler has refreshed and pinned both repository identities. It shares the
// exact same transaction lock, shutdown, snapshot and recovery engine as releases.
func (c *Controller) StartSource(request SourceRequest, builder SourceBuilder) (map[string]any, error) {
	if builder == nil {
		return nil, errors.New("source builder is not configured")
	}
	if c.reason != "" {
		return nil, errors.New(c.reason)
	}
	if err := privateDirectory(c.opts.stateDir()); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lock != nil {
		return nil, errors.New("another maintenance operation is active")
	}
	lock, err := manager.AcquireDataLock(c.opts.stateDir())
	if err != nil {
		return nil, errors.New("another maintenance worker is active")
	}
	release := true
	defer func() {
		if release {
			lock.Close()
		}
	}()
	if _, err := os.Lstat(c.opts.Data + ".maintenance"); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("maintenance or recovery is active")
	}
	if err := c.checkPending(); err != nil {
		return nil, err
	}
	hash, err := fileHash(c.opts.Binary)
	if err != nil || hash != c.currentHash {
		return nil, errors.New("installed executable changed; restart before source maintenance")
	}
	if len(request.Current) != 2 {
		return nil, errors.New("exactly two current upstream identities are required")
	}
	for _, name := range []string{"cliproxyapi", "cpa-manager-plus"} {
		current, ok := request.Current[name]
		if !ok || !sourceCommitPattern.MatchString(current.Commit) {
			return nil, errors.New("both current upstream identities are required")
		}
	}
	if len(request.Targets) == 0 || len(request.Targets) > 2 {
		return nil, errors.New("choose one or both fixed upstream projects")
	}
	targets := map[string]SourceTarget{}
	for name, value := range request.Current {
		targets[name] = value
	}
	changed := false
	for name, value := range request.Targets {
		if name != "cliproxyapi" && name != "cpa-manager-plus" {
			return nil, errors.New("unknown upstream project")
		}
		if !sourceCommitPattern.MatchString(value.Commit) || value.Version == "" {
			return nil, errors.New("upstream target must be pinned to a real commit")
		}
		changed = changed || request.Current[name].Commit != value.Commit
		targets[name] = value
	}
	if !changed {
		return nil, errors.New("selected upstreams already match the running program")
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	op := Operation{ID: id, Kind: "install", State: "fetching_source", FromVersion: c.opts.Version, TargetVersion: c.opts.Version, Options: c.opts, ParentPID: os.Getpid(), OriginalHash: hash, CreatedAt: stamp(), SourceRequest: &request, FromUpstreams: request.Current, TargetUpstreams: targets}
	recordSourceProgress(&op, "checking_tools")
	if err := privateDirectory(c.opts.operationDir(id)); err != nil {
		return nil, err
	}
	if err := saveOperation(&op); err != nil {
		return nil, err
	}
	c.lock = lock
	release = false
	c.active = id
	c.wg.Add(1)
	response := publicOperation(op)
	go func() { defer c.wg.Done(); c.prepareSource(op, builder) }()
	return response, nil
}

func (c *Controller) prepareSource(op Operation, builder SourceBuilder) {
	fail := func(err error) {
		op.State = "failed"
		op.Error = err.Error()
		op.FailureCode = sourceErrorCode(err)
		if f, e := os.OpenFile(filepath.Join(c.opts.operationDir(op.ID), "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); e == nil {
			fmt.Fprintln(f, err)
			f.Close()
		}
		_ = saveOperation(&op)
		c.mu.Lock()
		if c.lock != nil {
			c.lock.Close()
			c.lock = nil
		}
		c.active = ""
		c.mu.Unlock()
	}
	candidate, err := builder(c.ctx, *op.SourceRequest, c.opts.operationDir(op.ID), func(phase string) {
		if phase == "activity" && op.Progress != nil {
			op.Progress.LastActivityAt = stamp()
			_ = saveOperation(&op)
		} else if SourceStepIndex(phase) >= 0 {
			recordSourceProgress(&op, phase)
			_ = saveOperation(&op)
		}

	})
	if err != nil {
		fail(err)
		return
	}
	if candidate.Version != c.opts.Version || !sourceDigestPattern.MatchString(candidate.SourceDigest) || !inside(c.opts.operationDir(op.ID), candidate.Archive) || !inside(c.opts.operationDir(op.ID), candidate.Workspace) {
		fail(errors.New("source build returned an unrelated or invalid candidate"))
		return
	}
	for name, target := range op.TargetUpstreams {
		actual := candidate.CoreCommit
		if name == "cpa-manager-plus" {
			actual = candidate.ManagerCommit
		}
		if actual != target.Commit {
			fail(errors.New("validated candidate does not match both pinned upstream commits"))
			return
		}
	}
	sum, err := fileHash(candidate.Archive)
	if err != nil || !strings.EqualFold(sum, candidate.SHA256) {
		fail(errors.New("source artifact changed after validation"))
		return
	}
	if err := writeJSON(filepath.Join(c.opts.operationDir(op.ID), "source-result.json"), candidate); err != nil {
		fail(err)
		return
	}
	if op.Progress != nil {
		op.Progress.Completed = op.Progress.Total
		op.Progress.LastActivityAt = stamp()
	}
	op.TargetSourceDigest = candidate.SourceDigest
	op.SourceWorkspace = candidate.Workspace
	op.SourceCoreCommit = candidate.CoreCommit
	op.SourceManagerCommit = candidate.ManagerCommit
	op.TargetArgs = replaceSourceWorkspace(c.opts.Args, candidate.Workspace)
	if err := saveOperation(&op); err != nil {
		fail(err)
		return
	}
	c.prepare(op, productAsset(candidate))
}
func replaceSourceWorkspace(args []string, workspace string) []string {
	result := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == "--source-workspace" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--source-workspace=") {
			continue
		}
		result = append(result, args[i])
	}
	return append(result, "--source-workspace", workspace)
}

var sourceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var sourceDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func productAsset(c SourceCandidate) product.Asset {
	return product.Asset{Name: c.Name, SHA256: c.SHA256}
}

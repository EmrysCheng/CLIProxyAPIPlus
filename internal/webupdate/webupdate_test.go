package webupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cliproxyapi-plus/internal/product"
	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
)

func fixture(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	o := Options{Enabled: true, Binary: filepath.Join(root, executableName()), Config: filepath.Join(root, "config.yaml"), Data: filepath.Join(root, "data"), HealthURL: "http://127.0.0.1:8317", Version: "1.0.0", WorkDir: root}
	o.Auth = filepath.Join(o.Data, "auths")
	for _, p := range []string{o.Data, o.Auth} {
		if err := privateDirectory(p); err != nil {
			t.Fatal(err)
		}
	}
	for path, value := range map[string]string{o.Binary: "original binary", o.Config: "original config", filepath.Join(o.Data, "usage.sqlite"): "offline fixture database", filepath.Join(o.Auth, "account.json"): "private auth"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return o
}
func transaction(t *testing.T, o Options) Operation {
	t.Helper()
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if err = privateDirectory(o.operationDir(id)); err != nil {
		t.Fatal(err)
	}
	hash, err := fileHash(o.Binary)
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{ID: id, Kind: "install", State: "backing_up", Options: o, OriginalHash: hash, FromVersion: "1.0.0", TargetVersion: "1.1.0", CreatedAt: stamp()}
	if err = saveOperation(&op); err != nil {
		t.Fatal(err)
	}
	return op
}
func TestSnapshotPairedRestoreAndResumableRecovery(t *testing.T) {
	o := fixture(t)
	op := transaction(t, o)
	if err := snapshot(&op); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Binary, []byte("new binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.Auth, "account.json"), []byte("changed auth"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.Config, []byte("new config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restore(&op, op); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{o.Binary: "original binary", o.Config: "original config", filepath.Join(o.Auth, "account.json"): "private auth"} {
		got, e := os.ReadFile(path)
		if e != nil || string(got) != want {
			t.Fatalf("restore %s: %s %v", path, got, e)
		}
	}
	retained := op.FailedData
	if got, _ := os.ReadFile(filepath.Join(retained, "auths/account.json")); string(got) != "changed auth" {
		t.Fatal("failed data were not retained")
	}
	// Repeat after an interrupted restore: old retained directories must not be
	// deleted or overwritten. A new staging/retention identity is used each time.
	op.State = "recovery_required"
	if err := saveOperation(&op); err != nil {
		t.Fatal(err)
	}
	if err := exclusiveMarker(o.Data+".maintenance", op.ID); err != nil {
		t.Fatal(err)
	}
	if err := RunWorker(context.Background(), o.Data, o.Config, op.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(retained); err != nil {
		t.Fatal("recovery destroyed earlier retained data")
	}
	if _, err := os.Stat(o.Data + ".maintenance"); !os.IsNotExist(err) {
		t.Fatal("owned maintenance marker was not cleared")
	}
}
func TestSnapshotTamperingAndLiveLockPreventRestore(t *testing.T) {
	o := fixture(t)
	op := transaction(t, o)
	if err := snapshot(&op); err != nil {
		t.Fatal(err)
	}
	lock, err := manager.AcquireDataLock(o.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err = restore(&op, op); err == nil {
		t.Fatal("restored a live database")
	}
	_ = lock.Close()
	path := filepath.Join(snapshotPath(op), "data/auths/account.json")
	if err = os.WriteFile(path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = verifySnapshot(op); err == nil {
		t.Fatal("accepted a tampered snapshot")
	}
	if err = restore(&op, op); err == nil {
		t.Fatal("restored a tampered snapshot")
	}
	got, _ := os.ReadFile(o.Binary)
	if string(got) != "original binary" {
		t.Fatal("verification failure changed the current program")
	}
}
func TestInstallationAndOriginBoundaries(t *testing.T) {
	o := fixture(t)
	if err := validateInstallation(o); err != nil {
		t.Fatal(err)
	}
	other := o
	other.Auth = filepath.Dir(o.Data)
	if validateInstallation(other) == nil {
		t.Fatal("accepted external credentials")
	}
	other = o
	other.TLSCert = filepath.Join(filepath.Dir(o.Data), "external.crt")
	if validateInstallation(other) == nil {
		t.Fatal("accepted external TLS material")
	}
	other = o
	other.Binary = filepath.Join(o.Data, "app")
	if validateInstallation(other) == nil {
		t.Fatal("accepted data/install overlap")
	}
	for _, origin := range []string{"https://evil.example", "null", "https://same.example.evil"} {
		r := httptest.NewRequest("POST", "https://same.example/action", nil)
		r.Header.Set("Origin", origin)
		if sameOrigin(r) {
			t.Fatal(origin)
		}
	}
	r := httptest.NewRequest("POST", "https://same.example/action", nil)
	r.Header.Set("Origin", "https://same.example")
	if !sameOrigin(r) {
		t.Fatal("rejected same origin")
	}
	for _, path := range []string{"../binary", "/absolute", "a/../../b", "a\\b", "C:/binary", "a//b"} {
		if safeRelative(path) {
			t.Fatal(path)
		}
	}
	for _, raw := range []string{"http://release.example/a.zip", "https://user:pass@release.example/a.zip", "https://unlisted.example/a.zip", "https://github.com.evil/a.zip"} {
		if allowedDownload("https://release.example/index.json", raw) {
			t.Fatal(raw)
		}
	}
	if !allowedDownload("https://release.example/index.json", "https://release.example/a.zip") {
		t.Fatal("rejected configured origin")
	}
}
func zipFixture(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	file, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	archive := zip.NewWriter(file)
	for name, value := range entries {
		entry, e := archive.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = entry.Write([]byte(value)); e != nil {
			t.Fatal(e)
		}
	}
	if e = archive.Close(); e != nil {
		t.Fatal(e)
	}
	_ = file.Close()
	return path
}
func TestArchiveExtractionOnlyAcceptsSafeBinary(t *testing.T) {
	for _, unsafe := range []string{"../escape", "/absolute", "dir/../escape", "C:/binary", "dir\\escape"} {
		archive := zipFixture(t, map[string]string{executableName(): "binary", unsafe: "invalid"})
		dest := filepath.Join(t.TempDir(), "candidate")
		if extractBinary(archive, "release.zip", dest) == nil {
			t.Fatal(unsafe)
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatal("partial executable was retained")
		}
	}
	archive := zipFixture(t, map[string]string{executableName(): "binary", "docs/README.md": "notice"})
	dest := filepath.Join(t.TempDir(), "candidate")
	if err := extractBinary(archive, "release.zip", dest); err != nil {
		t.Fatal(err)
	}
	if bytes, err := os.ReadFile(dest); err != nil || string(bytes) != "binary" {
		t.Fatal(err)
	}
}

type roundtrip func(*http.Request) (*http.Response, error)

func (f roundtrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestBadDownloadNeverStopsServiceAndDuplicateActionBlocked(t *testing.T) {
	o := fixture(t)
	r, _ := product.NewReleases("https://release.example/index.json", "1.0.0")
	suffix := ".tar.gz"
	if runtime.GOOS == "windows" {
		suffix = ".zip"
	}
	name := "CLIProxyAPIPlus_1.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + "_portable" + suffix
	asset := product.Asset{Name: name, SHA256: strings.Repeat("a", 64), URL: "https://release.example/package"}
	encoded, _ := json.Marshal(product.Index{SchemaVersion: 1, Release: product.Release{Version: "1.1.0", SourceCommit: strings.Repeat("a", 40), Assets: []product.Asset{asset}}})
	started := make(chan struct{})
	release := make(chan struct{})
	r.Client = &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "index.json") {
			return response(string(encoded)), nil
		}
		close(started)
		select {
		case <-release:
			return response("bad package"), nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}
	stopped := make(chan struct{}, 1)
	c := New(context.Background(), o, r, func() { stopped <- struct{}{} })
	defer c.Close(false)
	input := `{"version":"1.1.0","sha256":"` + asset.SHA256 + `","confirmation":"1.1.0"}`
	post := func(body, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "https://local/plus-api/releases/operations/install", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		c.ServeHTTP(w, request)
		return w
	}
	if code := post(input, "https://evil.example").Code; code != 403 {
		t.Fatal(code)
	}
	if code := post(`{"version":"1.1.0"}`, "").Code; code != 409 {
		t.Fatal(code)
	}
	accepted := post(input, "")
	if accepted.Code != 202 {
		t.Fatalf("%d %s", accepted.Code, accepted.Body.String())
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download not started")
	}
	if code := post(input, "").Code; code != 409 {
		t.Fatal("duplicate action accepted", code)
	}
	close(release)
	c.wg.Wait()
	select {
	case <-stopped:
		t.Fatal("bad package stopped the original service")
	default:
	}
	public, _ := json.Marshal(c.Snapshot())
	if bytes.Contains(public, []byte(o.Config)) || bytes.Contains(public, []byte(o.Binary)) {
		t.Fatal("private installation paths leaked")
	}
	if !bytes.Contains(public, []byte("failed")) {
		t.Fatalf("missing failed state: %s", public)
	}
}
func TestDownloadChecksum(t *testing.T) {
	content := []byte("archive")
	hash := sha256.Sum256(content)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(content) }))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "download")
	asset := product.Asset{Name: "release.zip", URL: server.URL + "/download", SHA256: hex.EncodeToString(hash[:])}
	if err := download(context.Background(), server.URL+"/index", asset, path, server.Client()); err != nil {
		t.Fatal(err)
	}
}

func TestRecordWriterNeverPersistsAnUnreadableJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	if err := writeJSON(path, map[string]string{"state": "prepared"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, map[string]string{"huge": strings.Repeat("x", 8<<20)}); err == nil {
		t.Fatal("persisted an unreadable oversized journal")
	}
	var record map[string]string
	if err := readJSON(path, &record); err != nil || record["state"] != "prepared" {
		t.Fatal("oversize failure corrupted the last readable state", err)
	}
}

func TestJournalReaderAllowsAtomicReplacementWhileOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operation.json")
	if err := writeJSON(path, map[string]string{"state": "old"}); err != nil {
		t.Fatal(err)
	}
	reader, err := openRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err = writeJSON(path, map[string]string{"state": "new"}); err != nil {
		t.Fatal(err)
	}
	var latest map[string]string
	if err = readJSON(path, &latest); err != nil || latest["state"] != "new" {
		t.Fatal("new readers did not see the atomic replacement", err)
	}
	old, err := io.ReadAll(reader)
	if err != nil || !bytes.Contains(old, []byte("old")) {
		t.Fatal("an existing reader lost its stable old record", err)
	}
}

func TestLocalHTTPDownloadPolicyAndChecksum(t *testing.T) {
	content := []byte("local package")
	sum := sha256.Sum256(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(content) }))
	defer server.Close()
	asset := product.Asset{Name: "package.zip", SHA256: hex.EncodeToString(sum[:]), URL: server.URL + "/package.zip"}
	if err := download(context.Background(), server.URL+"/index.json", asset, filepath.Join(t.TempDir(), "blocked"), server.Client()); err == nil {
		t.Fatal("HTTP download without opt-in")
	}
	if err := download(context.Background(), server.URL+"/index.json", asset, filepath.Join(t.TempDir(), "package"), server.Client(), true); err != nil {
		t.Fatal(err)
	}
	asset.SHA256 = strings.Repeat("0", 64)
	if err := download(context.Background(), server.URL+"/index.json", asset, filepath.Join(t.TempDir(), "bad"), server.Client(), true); err == nil {
		t.Fatal("checksum bypassed")
	}
}

func TestLocalHTTPDownloadRedirectCannotLeaveOrigin(t *testing.T) {
	var contacted atomic.Bool
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted.Store(true) }))
	defer trap.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, trap.URL+"/package", http.StatusFound)
	}))
	defer server.Close()
	asset := product.Asset{Name: "package.zip", SHA256: strings.Repeat("a", 64), URL: server.URL + "/package"}
	if err := download(context.Background(), server.URL+"/index.json", asset, filepath.Join(t.TempDir(), "blocked"), server.Client(), true); err == nil {
		t.Fatal("cross-origin redirect accepted")
	}
	if contacted.Load() {
		t.Fatal("cross-origin server contacted")
	}
}

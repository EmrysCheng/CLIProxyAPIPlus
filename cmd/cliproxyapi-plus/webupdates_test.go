package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWebUpdateLifecycle operates exclusively on copied executables, disposable
// config/data and private HTTPS/loopback HTTP release servers. It never updates the user's install.
func TestWebUpdateLifecycle(t *testing.T) {
	if os.Getenv("CLIPROXY_PLUS_UPDATE_E2E") != "1" {
		t.Skip("opt-in real Portable update/rollback test")
	}
	reports := map[string]any{}
	var summary map[string]any
	for _, mode := range []string{"https", "loopback-http"} {
		t.Run(mode, func(t *testing.T) {
			report := runWebUpdateLifecycle(t, mode == "loopback-http")
			reports[mode] = report
			summary = map[string]any{}
			for key, value := range report {
				summary[key] = value
			}
		})
	}
	if t.Failed() {
		return
	}
	summary["cases"] = reports
	reportBytes, _ := json.MarshalIndent(summary, "", "  ")
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	if err := os.WriteFile(filepath.Join(root, ".build", "web-update-smoke-report.json"), reportBytes, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(reportBytes))
}

func runWebUpdateLifecycle(t *testing.T, localHTTP bool) map[string]any {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("CLIPROXY_PLUS_TEST_BINARY")
	if source == "" {
		source = filepath.Join(root, "dist", runtime.GOOS+"-"+runtime.GOARCH+"-portable", testExeName())
	}
	rawIdentity, e := exec.Command(source, "--version").Output()
	if e != nil {
		t.Fatal(e)
	}
	var initialIdentity struct {
		Version string `json:"version"`
		Source  string `json:"product_source_sha256"`
	}
	if e = json.Unmarshal(rawIdentity, &initialIdentity); e != nil {
		t.Fatal(e)
	}
	currentVersion := initialIdentity.Version
	var major, minor, patch int
	if n, _ := fmt.Sscanf(strings.TrimPrefix(currentVersion, "v"), "%d.%d.%d", &major, &minor, &patch); n != 3 {
		t.Fatal("invalid fixture base version")
	}
	upgradeVersion := fmt.Sprintf("%d.%d.%d-webtest.1", major, minor, patch+1)
	failedVersion := fmt.Sprintf("%d.%d.%d-webtest.1", major, minor, patch+2)
	work := t.TempDir()
	installed := filepath.Join(work, testExeName())
	config := filepath.Join(work, "config.yaml")
	data := filepath.Join(work, "data")
	copyTestBinary(t, source, installed)
	candidate := filepath.Join(work, "candidate-"+testExeName())
	failed := filepath.Join(work, "failed-"+testExeName())
	for _, build := range []struct{ out, pkg, flags string }{{candidate, "./cmd/cliproxyapi-plus", "-X main.Version=" + upgradeVersion + " -X main.SourceDigest=" + initialIdentity.Source}, {failed, "./tests/fixtures/update-failure", "-X main.Version=" + failedVersion + " -X main.SourceDigest=" + initialIdentity.Source}} {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags", build.flags, "-o", build.out, build.pkg)
		cmd.Dir = root
		if output, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("test candidate build: %v %s", e, output)
		}
	}
	makePackage := func(binary string) []byte {
		var result bytes.Buffer
		if runtime.GOOS != "windows" {
			gz := gzip.NewWriter(&result)
			tr := tar.NewWriter(gz)
			info, e := os.Stat(binary)
			if e != nil {
				t.Fatal(e)
			}
			if e = tr.WriteHeader(&tar.Header{Name: testExeName(), Mode: 0700, Size: info.Size(), Typeflag: tar.TypeReg}); e != nil {
				t.Fatal(e)
			}
			file, e := os.Open(binary)
			if e != nil {
				t.Fatal(e)
			}
			_, e = io.Copy(tr, file)
			_ = file.Close()
			if e != nil {
				t.Fatal(e)
			}
			if e = tr.Close(); e != nil {
				t.Fatal(e)
			}
			if e = gz.Close(); e != nil {
				t.Fatal(e)
			}
			return result.Bytes()
		}
		archive := zip.NewWriter(&result)
		entry, e := archive.Create(testExeName())
		if e != nil {
			t.Fatal(e)
		}
		file, e := os.Open(binary)
		if e != nil {
			t.Fatal(e)
		}
		_, e = io.Copy(entry, file)
		_ = file.Close()
		if e != nil {
			t.Fatal(e)
		}
		if e = archive.Close(); e != nil {
			t.Fatal(e)
		}
		return result.Bytes()
	}
	goodPackage, badPackage := makePackage(candidate), makePackage(failed)
	var mu sync.Mutex
	releaseVersion := upgradeVersion
	releaseSource := initialIdentity.Source
	releaseOnline := true
	archiveBytes := goodPackage
	var releaseServer *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !releaseOnline {
			http.Error(w, "fixture release source offline", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/package" {
			_, _ = w.Write(archiveBytes)
			return
		}
		sum := sha256.Sum256(archiveBytes)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "release": map[string]any{"version": releaseVersion, "product_source_sha256": releaseSource, "assets": []map[string]string{{"name": "CLIProxyAPIPlus_" + releaseVersion + "_" + runtime.GOOS + "_" + runtime.GOARCH + "_portable" + testArchiveSuffix(), "sha256": hex.EncodeToString(sum[:]), "url": releaseServer.URL + "/package"}}}})
	})
	sourceArgs := []string{}
	if localHTTP {
		releaseServer = httptest.NewServer(handler)
		sourceArgs = append(sourceArgs, "--allow-local-release-http")
	} else {
		releaseServer = httptest.NewTLSServer(handler)
		caFile := filepath.Join(work, "release-ca.pem")
		if err = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: releaseServer.Certificate().Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		sourceArgs = append(sourceArgs, "--release-ca-file", caFile)
	}
	defer releaseServer.Close()
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserve.Addr().String()
	_ = reserve.Close()
	base := "http://" + address
	env := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "PATH", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "HOME", "PATHEXT":
			env = append(env, entry)
		}
	}
	env = append(env, "WRITABLE_PATH="+work)
	init := exec.Command(installed, "--init", "--config", config, "--data-dir", data, "--listen", address)
	init.Env = env
	init.Dir = work
	if output, e := init.CombinedOutput(); e != nil {
		t.Fatalf("init: %v %s", e, output)
	}
	if err = os.WriteFile(filepath.Join(data, "snapshot-sentinel"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if localHTTP {
		blocked := exec.Command(installed, "--config", config, "--data-dir", data, "--web-updates", "--release-index", releaseServer.URL+"/index.json")
		blocked.Env, blocked.Dir = env, work
		output, err := blocked.CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("local HTTP requires --allow-local-release-http")) {
			t.Fatalf("HTTP without opt-in: %v %s", err, output)
		}
	}
	initialArgs := []string{"--config", config, "--data-dir", data, "--web-updates", "--release-index", releaseServer.URL + "/index.json"}
	initial := exec.Command(installed, append(initialArgs, sourceArgs...)...)
	initial.Env = env
	initial.Dir = work
	console, err := os.Create(filepath.Join(work, "initial-console.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer console.Close()
	initial.Stdout = console
	initial.Stderr = console
	if err = initial.Start(); err != nil {
		t.Fatal(err)
	}
	initialDone := make(chan error, 1)
	go func() { initialDone <- initial.Wait() }()
	t.Cleanup(func() {
		if t.Failed() {
			_ = filepath.WalkDir(data+".updates", func(path string, d os.DirEntry, e error) error {
				if e == nil && !d.IsDir() && (d.Name() == "worker.log" || d.Name() == "application-console.log" || d.Name() == "operation.json") {
					content, readErr := os.ReadFile(path)
					if readErr == nil {
						if len(content) > 12000 {
							content = content[len(content)-12000:]
						}
						t.Logf("diagnostic %s:\n%s", path, content)
					}
				}
				return nil
			})
		}
		// Validate executable paths against the unique disposable root before any
		// cleanup signal; never act on an arbitrary PID read from an unrelated file.
		pids := map[int]bool{initial.Process.Pid: true}
		entries, _ := os.ReadDir(data + ".updates")
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			for _, name := range []string{"worker-ready.json", "application-ready.json"} {
				var signal struct {
					PID int `json:"pid"`
				}
				bytes, e := os.ReadFile(filepath.Join(data+".updates", entry.Name(), name))
				if e == nil && json.Unmarshal(bytes, &signal) == nil && signal.PID > 0 {
					pids[signal.PID] = true
				}
			}
		}
		for pid := range pids {
			path, e := testProcessImage(pid)
			if e != nil {
				continue
			}
			rel, e := filepath.Rel(work, path)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				t.Logf("not stopping unknown process %d", pid)
				continue
			}
			p, e := os.FindProcess(pid)
			if e == nil {
				_ = p.Kill()
			}
		}
		time.Sleep(500 * time.Millisecond)
	})
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 2 * time.Second}
	adminBytes, err := os.ReadFile(filepath.Join(data, "admin.key"))
	if err != nil {
		t.Fatal(err)
	}
	admin := strings.TrimSpace(string(adminBytes))
	request := func(method, path, key string, payload any) (int, []byte, error) {
		var body io.Reader
		if payload != nil {
			bytes, e := json.Marshal(payload)
			if e != nil {
				return 0, nil, e
			}
			body = strings.NewReader(string(bytes))
		}
		req, e := http.NewRequest(method, base+path, body)
		if e != nil {
			return 0, nil, e
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", base)
		}
		response, e := client.Do(req)
		if e != nil {
			return 0, nil, e
		}
		defer response.Body.Close()
		bytes, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return response.StatusCode, bytes, e
	}
	wait := func(name string, check func() bool) {
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s; inspect test logs under %s", name, work)
	}
	wait("initial readiness", func() bool { code, _, e := request("GET", "/plus-api/ready", "", nil); return e == nil && code == 200 })
	for _, credential := range []string{"", strings.TrimSpace(string(mustReadTest(t, filepath.Join(data, "api.key")))), strings.TrimSpace(string(mustReadTest(t, filepath.Join(data, "core-management.key"))))} {
		code, _, e := request("POST", "/plus-api/releases/operations/install", credential, map[string]string{"version": upgradeVersion})
		if e != nil || code != 401 {
			t.Fatalf("update auth boundary: %d %v", code, e)
		}
	}
	code, body, e := request("GET", "/plus-api/releases/operations", admin, nil)
	if e != nil || code != 200 {
		t.Fatalf("capabilities %d %s %v", code, body, e)
	}
	var capabilities struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal(body, &capabilities)
	if !capabilities.Enabled {
		t.Fatal(capabilities.Reason)
	}
	install := func(version string, archive []byte) string {
		t.Helper()
		code, body, e := request("POST", "/plus-api/releases/check", admin, nil)
		if e != nil || code != 200 {
			t.Fatalf("check %d %s %v", code, body, e)
		}
		sum := sha256.Sum256(archive)
		code, body, e = request("POST", "/plus-api/releases/operations/install", admin, map[string]any{"version": version, "sha256": hex.EncodeToString(sum[:]), "confirmation": version})
		if e != nil || code != 202 {
			t.Fatalf("install %d %s %v", code, body, e)
		}
		var accepted struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body, &accepted) != nil || accepted.ID == "" {
			t.Fatal(string(body))
		}
		return accepted.ID
	}
	operation := func(id, state, version string) {
		t.Helper()
		wait(state+" "+id, func() bool {
			code, body, e := request("GET", "/plus-api/releases/operations", admin, nil)
			if e != nil || code != 200 {
				return false
			}
			var status struct {
				History []struct{ ID, State string } `json:"history"`
			}
			if json.Unmarshal(body, &status) != nil {
				return false
			}
			found := false
			for _, item := range status.History {
				if item.ID == id {
					if item.State == "recovery_required" {
						t.Fatalf("unexpected recovery required: %s", body)
					}
					found = item.State == state
				}
			}
			if !found {
				return false
			}
			code, body, e = request("GET", "/plus-api/version", "", nil)
			if e != nil || code != 200 {
				return false
			}
			var identity struct{ Version string }
			_ = json.Unmarshal(body, &identity)
			_, marker := os.Stat(data + ".maintenance")
			return identity.Version == version && os.IsNotExist(marker)
		})
	}
	wait("automatic update reminder", func() bool {
		code, body, err := request("GET", "/plus-api/releases", admin, nil)
		var status struct {
			State string
			Stale bool
		}
		return err == nil && code == 200 && json.Unmarshal(body, &status) == nil && status.State == "update_available" && !status.Stale
	})
	// Matching archive hash alone is not enough: reject a claimed source that
	// differs from the executable identity, without stopping the old service.
	mu.Lock()
	releaseSource = strings.Repeat("0", 64)
	mu.Unlock()
	mismatchID := install(upgradeVersion, goodPackage)
	operation(mismatchID, "failed", currentVersion)
	mu.Lock()
	releaseSource = initialIdentity.Source
	mu.Unlock()
	upgradeID := install(upgradeVersion, goodPackage)
	t.Log("upgrade operation", upgradeID)
	operation(upgradeID, "succeeded", upgradeVersion)
	select {
	case e := <-initialDone:
		if e != nil {
			t.Fatalf("original process did not stop cleanly: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old executable still running")
	}
	if err = os.WriteFile(filepath.Join(data, "snapshot-sentinel"), []byte("after upgrade"), 0600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	releaseOnline = false
	mu.Unlock()
	code, body, e = request("POST", "/plus-api/releases/operations/rollback", admin, map[string]any{"operation_id": upgradeID, "confirmation": currentVersion, "accept_data_loss": true})
	if e != nil || code != 202 {
		t.Fatalf("rollback %d %s %v", code, body, e)
	}
	var rollback struct{ ID string }
	_ = json.Unmarshal(body, &rollback)
	t.Log("manual rollback operation", rollback.ID)
	operation(rollback.ID, "rolled_back", currentVersion)
	if bytes := mustReadTest(t, filepath.Join(data, "snapshot-sentinel")); string(bytes) != "before" {
		t.Fatalf("rollback did not restore matching snapshot: %s", bytes)
	}
	mu.Lock()
	releaseOnline = true
	releaseVersion = failedVersion
	archiveBytes = badPackage
	mu.Unlock()
	failedID := install(failedVersion, badPackage)
	t.Log("failed migration operation", failedID)
	operation(failedID, "rolled_back", currentVersion)
	if _, e = os.Stat(filepath.Join(data, "migration-touched")); !os.IsNotExist(e) {
		t.Fatal("failed migration output leaked into restored data")
	}
	report := map[string]any{"product_source_sha256": initialIdentity.Source, "passed": true, "time": time.Now().UTC().Format(time.RFC3339), "native_target": runtime.GOOS + "/" + runtime.GOARCH, "upgrade": upgradeID, "rollback": rollback.ID, "migration_failure_auto_rollback": failedID, "disposable_root": work, "checks": []string{"DB-backed three-credential boundary", "explicit release transport policy and persisted restart arguments", "automatic update reminder state", "archive SHA256 and candidate identity", "mismatched source rejected before stopping service", "rollback succeeds with release source offline", "clean old-process exit before replacement", "actual executable upgrade and new-process health", "page API rollback restores matching data", "migration failure automatically restores old binary and data"}}
	report["loopback_http"] = localHTTP
	return report
}
func testExeName() string {
	if runtime.GOOS == "windows" {
		return "cliproxyapi-plus.exe"
	}
	return "cliproxyapi-plus"
}
func mustReadTest(t *testing.T, path string) []byte {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes
}
func copyTestBinary(t *testing.T, source, dest string) {
	t.Helper()
	input, e := os.Open(source)
	if e != nil {
		t.Fatal(e)
	}
	defer input.Close()
	output, e := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(output, input)
	closeErr := output.Close()
	if e != nil || closeErr != nil {
		t.Fatal(fmt.Errorf("copy binary: %v %v", e, closeErr))
	}
}

func testArchiveSuffix() string {
	if runtime.GOOS == "windows" {
		return ".zip"
	}
	return ".tar.gz"
}

func TestWebUpdateHealthUsesActualListenerAddress(t *testing.T) {
	for _, tc := range []struct{ host, want string }{{"127.0.0.1", "127.0.0.1:8317"}, {"127.0.0.2", "127.0.0.2:8317"}, {"192.0.2.5", "192.0.2.5:8317"}, {"0.0.0.0", "127.0.0.1:8317"}, {"::", "[::1]:8317"}, {"::1", "[::1]:8317"}, {"2001:db8::5", "[2001:db8::5]:8317"}} {
		got, err := webUpdateHealthAddress(tc.host, 8317)
		if err != nil || got != tc.want {
			t.Errorf("%s -> %s %v; want %s", tc.host, got, err, tc.want)
		}
	}
	for _, host := range []string{"", "localhost", "example.com"} {
		if _, err := webUpdateHealthAddress(host, 8317); err == nil {
			t.Errorf("accepted unsupported listener %q", host)
		}
	}
	if _, err := webUpdateHealthAddress("127.0.0.1", 0); err == nil {
		t.Fatal("accepted dynamic port")
	}
}

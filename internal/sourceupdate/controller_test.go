package sourceupdate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cliproxyapi-plus/internal/product"
	"cliproxyapi-plus/internal/webupdate"
)

type metadataTrip func(*http.Request) (*http.Response, error)

func (f metadataTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	opts := Options{Enabled: true, Workspace: root, SourceDigest: strings.Repeat("a", 64), LockDigest: strings.Repeat("b", 64), Version: "1.0.0", Current: map[string]webupdate.SourceTarget{"cliproxyapi": {Version: "v8.0.53", Commit: strings.Repeat("a", 40)}, "cpa-manager-plus": {Version: "v1.14.10", Commit: strings.Repeat("b", 40)}}}
	raw, _ := json.Marshal(kitManifest{Schema: 1, SourceDigest: opts.SourceDigest, LockDigest: opts.LockDigest, Records: map[string]string{"placeholder": "placeholder"}})
	if err := os.WriteFile(filepath.Join(root, "source-kit.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return opts
}
func mockMetadata(c *Controller, fail bool) *atomic.Int32 {
	contacts := &atomic.Int32{}
	c.client = &http.Client{Transport: metadataTrip(func(r *http.Request) (*http.Response, error) {
		contacts.Add(1)
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			return nil, errors.New("untrusted metadata destination")
		}
		if fail {
			return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("rate limited"))}, nil
		}
		body := ""
		switch r.URL.Path {
		case "/repos/router-for-me/CLIProxyAPI/releases/latest":
			body = `{"tag_name":"v8.0.54"}`
		case "/repos/router-for-me/CLIProxyAPI/commits/v8.0.54":
			body = `{"sha":"` + strings.Repeat("c", 40) + `"}`
		case "/repos/seakee/CPA-Manager-Plus/releases/latest":
			body = `{"tag_name":"v1.14.10"}`
		case "/repos/seakee/CPA-Manager-Plus/commits/v1.14.10":
			body = `{"sha":"` + strings.Repeat("b", 40) + `"}`
		default:
			return nil, errors.New("unexpected repository or ref")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	c.toolResolver = func(context.Context, string) (Tools, ToolStatus) {
		return Tools{}, ToolStatus{Ready: true, Missing: []string{}, Versions: map[string]string{"Python": "Python 3.12.14", "Go": "go1.26.10", "Node": "v24"}}
	}
	return contacts
}
func TestFixedRepositoriesAndIndependentReminders(t *testing.T) {
	opts := testOptions(t)
	opts.Enabled = false
	c := New(opts, nil)
	contacts := mockMetadata(c, false)
	status := c.Check(context.Background())
	if contacts.Load() != 4 || len(status.Projects) != 2 {
		t.Fatal(status)
	}
	if status.Enabled || status.Mode != "direct-upstream-source" {
		t.Fatal(status)
	}
	if status.Projects[0].State != "update_available" || status.Projects[0].Stale || status.Projects[1].State != "up_to_date" {
		t.Fatal(status)
	}
	if status.Projects[0].LatestCommit != strings.Repeat("c", 40) {
		t.Fatal("target was not commit-pinned")
	}
	mockMetadata(c, true)
	status = c.Check(context.Background())
	for _, p := range status.Projects {
		if !p.Stale || p.State != "check_failed" {
			t.Fatal("failed check advertised success")
		}
	}
}
func TestSourceActionTrustAndNoLiveMutationOnBuildFailure(t *testing.T) {
	opts := testOptions(t)
	root := t.TempDir()
	data := filepath.Join(root, "data")
	auth := filepath.Join(data, "auth")
	if err := os.MkdirAll(auth, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "program")
	config := filepath.Join(root, "config.yaml")
	for _, path := range []string{binary, config} {
		if err := os.WriteFile(path, []byte("existing program/config"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	releases, _ := product.NewReleases("", "1.0.0")
	stopped := atomic.Bool{}
	maintenance := webupdate.New(context.Background(), webupdate.Options{Enabled: true, Binary: binary, Config: config, Data: data, Auth: auth, HealthURL: "http://127.0.0.1:8317", Version: "1.0.0", WorkDir: root}, releases, func() { stopped.Store(true) })
	defer maintenance.Close(false)
	c := New(opts, maintenance)
	mockMetadata(c, false)
	started := make(chan webupdate.SourceRequest, 1)
	finish := make(chan struct{})
	c.builder = func(ctx context.Context, request webupdate.SourceRequest, _ string, phase func(string)) (webupdate.SourceCandidate, error) {
		started <- request
		phase("validating_source")
		select {
		case <-finish:
		case <-ctx.Done():
		}
		return webupdate.SourceCandidate{}, errors.New("intentional incompatible patch fixture")
	}
	post := func(body string, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest("POST", "http://127.0.0.1:8317/plus-api/releases/upstreams/install", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response := httptest.NewRecorder()
		c.ServeHTTP(response, request)
		return response
	}
	input := map[string]any{"project": "cliproxyapi", "targets": map[string]string{"cliproxyapi": strings.Repeat("c", 40)}, "confirmation": "CLIProxyAPI v8.0.54", "trust_source_code": true}
	raw, _ := json.Marshal(input)
	if response := post(string(raw), "http://attacker.example"); response.Code != 403 {
		t.Fatal(response.Code)
	}
	input["trust_source_code"] = false
	raw, _ = json.Marshal(input)
	if response := post(string(raw), ""); response.Code != 409 {
		t.Fatal(response.Code)
	}
	input["trust_source_code"] = true
	input["url"] = "https://attacker.example/package"
	raw, _ = json.Marshal(input)
	if response := post(string(raw), ""); response.Code != 400 {
		t.Fatal("arbitrary source URL accepted")
	}
	delete(input, "url")
	raw, _ = json.Marshal(input)
	response := post(string(raw), "")
	if response.Code != 202 {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	select {
	case request := <-started:
		if len(request.Targets) != 1 || request.Current["cpa-manager-plus"].Commit != strings.Repeat("b", 40) {
			t.Fatal("unselected partner changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source build not started")
	}
	if second := post(string(raw), ""); second.Code != 409 {
		t.Fatal("concurrent source transaction accepted")
	}
	close(finish)
	failed := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		state := maintenance.Snapshot()
		history := state["history"].([]map[string]any)
		if len(history) > 0 && history[0]["state"] == "failed" {
			failed = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !failed {
		t.Fatal("source failure did not become terminal")
	}
	if stopped.Load() {
		t.Fatal("incompatible source build stopped live service")
	}
	content, err := os.ReadFile(binary)
	if err != nil || string(content) != "existing program/config" {
		t.Fatal("live binary was changed")
	}
	if _, err := os.Stat(data + ".maintenance"); !os.IsNotExist(err) {
		t.Fatal("failed build entered downtime")
	}
}
func TestConcurrentCheckCannotProvideInstallableStaleTargets(t *testing.T) {
	c := New(testOptions(t), nil)
	mockMetadata(c, false)
	c.Check(context.Background())
	c.checking.Lock()
	defer c.checking.Unlock()
	status := c.Check(context.Background())
	for _, p := range status.Projects {
		if !p.Stale {
			t.Fatal("in-flight check returned installable cache")
		}
	}
}

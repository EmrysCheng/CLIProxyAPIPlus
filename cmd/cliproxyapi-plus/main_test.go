package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAddress(t *testing.T) {
	for _, bad := range []string{"localhost:8317", ":8317", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:8317evil", "127.0.0.1:08317"} {
		if _, _, err := parseAddress(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, _, err := parseAddress("[::1]:8317"); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	data := filepath.Join(dir, "data")
	if err := initialize(cfg, data, "127.0.0.1:8317"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := initialize(cfg, data, "127.0.0.1:8318"); err == nil {
		t.Fatal("overwrote existing installation")
	}
	after, _ := os.ReadFile(cfg)
	if string(before) != string(after) {
		t.Fatal("existing configuration changed")
	}
	keys := map[string]bool{}
	for _, name := range []string{"admin.key", "api.key", "core-management.key"} {
		value, err := readCredential(filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
		if keys[value] {
			t.Fatal("credentials are not distinct")
		}
		keys[value] = true
	}
}

// This opt-in test starts the actual SDK, embedded Manager and a mock provider.
// It does not contact a paid provider or start either original service executable.
func TestSinglePortEndToEnd(t *testing.T) {
	if os.Getenv("CLIPROXY_PLUS_E2E") != "1" {
		t.Skip("set CLIPROXY_PLUS_E2E=1 after preparing and building the real frontend")
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-test-key" {
			http.Error(w, "upstream auth", 401)
			return
		}
		var request struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"p0-stream\",\"object\":\"chat.completion.chunk\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: {\"id\":\"p0-stream\",\"object\":\"chat.completion.chunk\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"p0-chat","object":"chat.completion","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer provider.Close()
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserve.Addr().String()
	_ = reserve.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	data := filepath.Join(dir, "data")
	if err := initialize(cfg, data, address); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(file, "\nopenai-compatibility:\n  - name: p0-mock\n    base-url: %q\n    api-key-entries:\n      - api-key: upstream-test-key\n    models:\n      - name: mock-model\n        alias: p0-model\n", provider.URL+"/v1")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, []string{"--config", cfg, "--data-dir", data}) }()
	defer func() {
		cancel()
		select {
		case err := <-finished:
			if err != nil {
				t.Errorf("shutdown: %v", err)
			}
		case <-time.After(50 * time.Second):
			t.Error("shutdown did not finish")
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second}
	base := "http://" + address
	request := func(method, path, key, body string) (int, []byte) {
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, bytes
	}
	deadline := time.Now().Add(35 * time.Second)
	for {
		response, err := client.Get(base + "/plus-api/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case err := <-finished:
			finished <- err
			t.Fatalf("server exited before readiness: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	admin, _ := readCredential(filepath.Join(data, "admin.key"))
	api, _ := readCredential(filepath.Join(data, "api.key"))
	coreKey, _ := readCredential(filepath.Join(data, "core-management.key"))
	code, html := request("GET", "/management.html", "", "")
	if code != 200 || len(html) < 100000 {
		t.Fatalf("real Manager frontend is not embedded: status=%d bytes=%d", code, len(html))
	}
	code, body := request("GET", "/v0/management/config", admin, "")
	if code != 200 {
		t.Fatalf("Manager -> in-process core: %d %s", code, body)
	}
	for _, key := range []string{"", api, coreKey} {
		code, _ = request("GET", "/v0/management/config", key, "")
		if code != 401 {
			t.Fatalf("management credential boundary: status=%d", code)
		}
	}
	code, _ = request("GET", "/v1/models", admin, "")
	if code != 401 {
		t.Fatalf("admin key was accepted on data plane: %d", code)
	}
	code, body = request("GET", "/v1/models", api, "")
	if code != 200 || !strings.Contains(string(body), "p0-model") {
		t.Fatalf("models: %d %s", code, body)
	}
	code, body = request("GET", "/v8/management/config/api-keys/openai-compatibility", admin, "")
	if code != 200 || !strings.Contains(string(body), "p0-mock") {
		t.Fatalf("v8 provider group read: %d %s", code, body)
	}
	code, body = request("POST", "/v1/chat/completions", api, `{"model":"p0-model","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(string(body), "hello") {
		t.Fatalf("chat: %d %s", code, body)
	}
	code, body = request("POST", "/v1/chat/completions", api, `{"model":"p0-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(string(body), "[DONE]") || !strings.Contains(string(body), "hello") {
		t.Fatalf("SSE: %d %s", code, body)
	}
	code, _ = request("GET", "/v8/management/config/upstreams", admin, "")
	if code != 404 {
		t.Fatalf("unapproved v8 route exposed: %d", code)
	}
	deadline = time.Now().Add(12 * time.Second)
	for {
		code, body = request("GET", "/v0/management/usage", admin, "")
		var usage struct {
			TotalRequests int `json:"total_requests"`
		}
		parseErr := json.Unmarshal(body, &usage)
		if code == 200 && parseErr == nil && usage.TotalRequests >= 2 && strings.Contains(string(body), "p0-model") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage was not persisted by embedded collector: %d %s", code, body)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Product releases share the Manager's stored admin verifier.
	for _, key := range []string{"", api, coreKey} {
		code, _ = request("GET", "/plus-api/releases", key, "")
		if code != 401 {
			t.Fatalf("product auth accepted non-admin: %d", code)
		}
	}
	code, body = request("GET", "/plus-api/releases", admin, "")
	if code != 200 || !strings.Contains(string(body), `"configured":false`) {
		t.Fatalf("product release status: %d %s", code, body)
	}
	original, _ := os.ReadFile(cfg)
	_, pinnedPort, _ := parseAddress(address)
	bad := strings.Replace(string(original), fmt.Sprintf("port: %d", pinnedPort), fmt.Sprintf("port: %d", pinnedPort%60000+1), 1)
	code, body = request("PUT", "/v0/management/config.yaml", admin, bad)
	if code < 400 {
		t.Fatalf("unsafe config save succeeded: %d %s", code, body)
	}
	after, _ := os.ReadFile(cfg)
	if string(after) != string(original) {
		t.Fatal("rejected configuration changed the disk file")
	}
	code, _ = request("GET", "/plus-api/ready", "", "")
	if code != 200 {
		t.Fatal("rejected save disrupted runtime")
	}
	t.Log("PASS: one listener; real Manager frontend; distinct admin/API/internal credentials; in-process core management; mock-provider JSON + SSE; SQLite usage; bounded shutdown")
}

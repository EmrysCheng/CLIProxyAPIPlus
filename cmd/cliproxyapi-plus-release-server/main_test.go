package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersionAndGracefulShutdown(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"role":"local-release-server"`) {
		t.Fatal(output.String())
	}
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserve.Addr().String()
	reserve.Close()
	root := t.TempDir()
	releases := filepath.Join(root, "releases")
	if err := os.Mkdir(releases, 0700); err != nil {
		t.Fatal(err)
	}
	name := "CLIProxyAPIPlus_1.2.3_windows_amd64_portable.zip"
	data := []byte("test package")
	sum := sha256.Sum256(data)
	if err := os.WriteFile(filepath.Join(releases, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	index := map[string]any{"schema_version": 1, "release": map[string]any{"version": "1.2.3", "product_source_sha256": strings.Repeat("a", 64), "assets": []map[string]string{{"name": name, "sha256": hex.EncodeToString(sum[:]), "url": "http://" + address + "/" + name}}}}
	raw, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(releases, "index.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"schema_version": 1, "listen": address, "release_dir": "releases", "log_file": "logs/server.log"}
	raw, _ = json.Marshal(config)
	path := filepath.Join(root, "release-server.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"--config", path, "--check"}, &output); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"--config", path}, &output) }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("server did not become ready")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if !strings.Contains(output.String(), "Index: http://"+address+"/index.json") {
		t.Fatal(output.String())
	}
	if response, err := client.Get("http://" + address + "/healthz"); err == nil {
		response.Body.Close()
		t.Fatal("listener remained active after shutdown")
	}
}

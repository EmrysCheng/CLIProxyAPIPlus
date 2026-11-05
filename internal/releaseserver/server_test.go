package releaseserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cliproxyapi-plus/internal/product"
)

func releaseFixture(t *testing.T, address string) (string, product.Index, []byte) {
	t.Helper()
	root := t.TempDir()
	content := []byte("portable package test fixture")
	name := "CLIProxyAPIPlus_1.2.3_windows_amd64_portable.zip"
	sum := sha256.Sum256(content)
	index := product.Index{SchemaVersion: 1, Release: product.Release{Version: "1.2.3", SourceDigest: strings.Repeat("a", 64), Assets: []product.Asset{{Name: name, SHA256: hex.EncodeToString(sum[:]), URL: "http://" + address + "/" + name}}}}
	if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
		t.Fatal(err)
	}
	writeFixtureIndex(t, root, index)
	return root, index, content
}
func writeFixtureIndex(t *testing.T, root string, index product.Index) {
	t.Helper()
	raw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyServingAndConfinement(t *testing.T) {
	address := "127.0.0.1:26973"
	root, index, content := releaseFixture(t, address)
	var logs strings.Builder
	handler, err := NewHandler(root, address, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	for _, name := range []string{"release-server.json", "secret.key", "CLIProxyAPIPlus_9.9.9_windows_amd64_portable.zip"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("not public"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		method, path string
		status       int
	}{{"GET", "/index.json", 200}, {"GET", "/healthz", 200}, {"HEAD", "/" + index.Release.Assets[0].Name, 200}, {"GET", "/", 404}, {"GET", "/release-server.json", 404}, {"GET", "/secret.key", 404}, {"GET", "/logs/release-server.log", 404}, {"GET", "/../release-server.json", 404}, {"GET", "/%2e%2e/secret.key", 404}, {"GET", "/CLIProxyAPIPlus_9.9.9_windows_amd64_portable.zip", 404}, {"GET", "/index.json?secret", 404}, {"POST", "/index.json", 405}, {"PUT", "/index.json", 405}} {
		request := httptest.NewRequest(item.method, "http://"+address+item.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatalf("%s %s: %d %s", item.method, item.path, response.Code, response.Body.String())
		}
		if item.method == "HEAD" && response.Body.Len() != 0 {
			t.Fatal("HEAD returned body")
		}
		if strings.Contains(response.Body.String(), "not public") {
			t.Fatal("private file exposed")
		}
	}
	request := httptest.NewRequest("GET", "http://"+address+"/"+index.Release.Assets[0].Name, nil)
	request.Header.Set("Range", "bytes=0-7")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 206 || response.Body.String() != string(content[:8]) {
		t.Fatalf("range support: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), "status=206") {
		t.Fatal("access log lost actual range status")
	}
	request = httptest.NewRequest("GET", "http://"+address+"/index.json", nil)
	request.Host = "attacker.example"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatal("untrusted Host accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "http://"+address+"/index.json", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 503 || strings.Contains(response.Body.String(), root) {
		t.Fatal("invalid index should fail closed without paths")
	}
}

func TestRejectBadBundlesAndSymlinks(t *testing.T) {
	address := "127.0.0.1:26973"
	for _, mutation := range []string{"checksum", "origin", "duplicate", "source", "name", "empty"} {
		t.Run(mutation, func(t *testing.T) {
			root, index, _ := releaseFixture(t, address)
			switch mutation {
			case "checksum":
				index.Release.Assets[0].SHA256 = strings.Repeat("0", 64)
			case "origin":
				index.Release.Assets[0].URL = "http://127.0.0.1:26974/package"
			case "duplicate":
				index.Release.Assets = append(index.Release.Assets, index.Release.Assets[0])
			case "source":
				index.Release.SourceDigest = "development"
			case "name":
				index.Release.Assets[0].Name = "../secret"
			case "empty":
				index.Release.Assets = nil
			}
			writeFixtureIndex(t, root, index)
			handler, err := NewHandler(root, address, nil)
			if err == nil {
				handler.Close()
				t.Fatal("invalid bundle accepted")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		root, index, content := releaseFixture(t, address)
		outside := filepath.Join(t.TempDir(), "outside.zip")
		if err := os.WriteFile(outside, content, 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, index.Release.Assets[0].Name)
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable for test account: %v", err)
		}
		handler, err := NewHandler(root, address, nil)
		if err == nil {
			handler.Close()
			t.Fatal("package symlink accepted")
		}
	})
}

func TestApplicationClientReadsPackagedServer(t *testing.T) {
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	address := server.Listener.Addr().String()
	root, _, _ := releaseFixture(t, address)
	handler, err := NewHandler(root, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	server.Config.Handler = handler
	server.Start()
	releases, err := product.NewReleasesWithPolicy(server.URL+"/index.json", "1.0.0", product.ReleaseSourcePolicy{AllowLocalHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if status := releases.Check(context.Background()); status.Error != "" || status.State != "update_available" {
		t.Fatalf("application update client: %+v", status)
	}
	response, err := server.Client().Get(server.URL + "/" + releases.Snapshot().Latest.Assets[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	sum := sha256.New()
	_, _ = io.Copy(sum, response.Body)
	if hex.EncodeToString(sum.Sum(nil)) != releases.Snapshot().Latest.Assets[0].SHA256 {
		t.Fatal("download digest mismatch")
	}
}

func TestConfigAndLogRotation(t *testing.T) {
	for _, address := range []string{"127.0.0.1:26973", "[::1]:26973"} {
		if err := ValidateListen(address); err != nil {
			t.Fatal(err)
		}
	}
	for _, address := range []string{"localhost:26973", "0.0.0.0:26973", "192.168.1.2:26973", "127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:26973/path"} {
		if ValidateListen(address) == nil {
			t.Fatal(address)
		}
	}
	root := t.TempDir()
	path := filepath.Join(root, "release-server.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"listen":"127.0.0.1:26973","release_dir":"releases","log_file":"logs/server.log"}`), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.ReleaseDir != filepath.Join(root, "releases") || config.LogFile != filepath.Join(root, "logs", "server.log") {
		t.Fatal("paths are not config-relative")
	}
	writer, err := OpenLog(config.LogFile, 32)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := writer.Write([]byte("a short log line\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(config.LogFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected active plus three backups: %v", entries)
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Size() > 32 {
			t.Fatal("log rotation exceeded limit")
		}
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"listen":"127.0.0.1:26973","release_dir":"releases","log_file":"logs/server.log","unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown config silently ignored")
	}
}

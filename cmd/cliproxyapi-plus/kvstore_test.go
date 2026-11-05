package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestWebUpdatesRejectExternalKVStoreBeforeStartup(t *testing.T) {
	if _, ok := any(&config.Config{}).(interface{ EffectiveKVStorePath() string }); !ok {
		t.Skip("pinned core predates the optional KVStore API")
	}
	root := t.TempDir()
	file := filepath.Join(root, "config.yaml")
	data := filepath.Join(root, "data")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	if err = initialize(file, data, address); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external.db")
	raw = append(raw, []byte(fmt.Sprintf("kv-store:\n  path: %q\n", filepath.ToSlash(external)))...)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = run(ctx, []string{"--config", file, "--data-dir", data, "--web-updates"})
	if err == nil || !strings.Contains(err.Error(), "kv-store.path inside data-dir") {
		t.Fatalf("external KVStore not rejected before startup: %v", err)
	}
	if _, err = os.Stat(external); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("startup opened external database", err)
	}
}

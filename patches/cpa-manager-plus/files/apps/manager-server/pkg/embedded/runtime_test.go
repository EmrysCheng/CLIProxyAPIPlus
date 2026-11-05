package embedded

import (
	"context"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestEmbeddedRuntimeDrainsAndReleasesDatabaseLock(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	ready := make(chan struct{})
	tr := roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}, nil
	})
	go func() {
		done <- Run(ctx, Options{DataDir: dir, AdminKey: strings.Repeat("a", 64), ManagementKey: strings.Repeat("b", 64), Transport: tr}, func(http.Handler) { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("not ready")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(35 * time.Second):
		t.Fatal("drain timeout")
	}
	lock, err := processlock.Acquire(filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal("database lock leaked", err)
	}
	_ = lock.Close()
}
func TestStandaloneEarlyCancellationDoesNotLeakListenerOrLock(t *testing.T) {
	for i := 0; i < 3; i++ {
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cfg := config.Config{HTTPAddr: "127.0.0.1:0", DataDir: dir, DBPath: filepath.Join(dir, "usage.sqlite"), DataKeyPath: filepath.Join(dir, "data.key"), UsageArchiveDir: filepath.Join(dir, "archives"), AdminKey: strings.Repeat("x", 64)}
		done := make(chan error, 1)
		go func() { done <- RunStandalone(ctx, cfg) }()
		select {
		case <-done:
		case <-time.After(35 * time.Second):
			t.Fatal("early cancellation stuck")
		}
		lock, err := processlock.Acquire(cfg.DBPath)
		if err != nil {
			t.Fatal("startup lock leaked", err)
		}
		_ = lock.Close()
	}
}

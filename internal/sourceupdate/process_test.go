package sourceupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestOwnedProcessHelper(t *testing.T) {
	mode := os.Getenv("PLUS_SOURCE_PROCESS_HELPER")
	if mode == "" {
		return
	}
	marker := os.Getenv("PLUS_SOURCE_PROCESS_MARKER")
	if mode == "child" {
		for {
			_ = os.WriteFile(marker, []byte(fmt.Sprint(time.Now().UnixNano())), 0600)
			time.Sleep(20 * time.Millisecond)
		}
	}
	gate := os.Getenv("PLUS_SOURCE_PROCESS_GATE")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(2)
		}
		time.Sleep(10 * time.Millisecond)
	}
	executable, _ := os.Executable()
	child := exec.Command(executable, "-test.run=^TestOwnedProcessHelper$")
	child.Env = append(os.Environ(), "PLUS_SOURCE_PROCESS_HELPER=child")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	_ = child.Wait()
}
func TestOwnedCancellationStopsBuildDescendants(t *testing.T) {
	root := t.TempDir()
	gate := filepath.Join(root, "owned.gate")
	marker := filepath.Join(root, "heartbeat")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestOwnedProcessHelper$")
	command.Env = append(os.Environ(), "PLUS_SOURCE_PROCESS_HELPER=parent", "PLUS_SOURCE_PROCESS_GATE="+gate, "PLUS_SOURCE_PROCESS_MARKER="+marker)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runOwned(ctx, command, gate) }()
	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			ready = true
			break
		}
		select {
		case err := <-done:
			t.Fatalf("owned worker exited: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatal("owned descendant never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation reported success")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("owned build tree did not stop")
	}
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("build descendant survived cancellation")
	}
}

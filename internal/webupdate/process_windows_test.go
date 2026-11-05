//go:build windows

package webupdate

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutableReplacementWaitsForTransientWindowsShareLock(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "incoming")
	dest := filepath.Join(root, "installed")
	if err := os.WriteFile(source, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}()
	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- renameFile(source, dest) }()
	time.Sleep(180 * time.Millisecond)
	_ = windows.CloseHandle(handle)
	handle = 0
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 150*time.Millisecond {
		t.Fatal("did not wait for the controlled sharing lock")
	}
	if content, _ := os.ReadFile(dest); string(content) != "new" {
		t.Fatal("replacement did not complete")
	}
}

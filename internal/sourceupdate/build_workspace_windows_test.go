//go:build windows

package sourceupdate

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSourceAcceptanceCleanupWaitsForTransientNativeFileReader(t *testing.T) {
	job := filepath.Join(t.TempDir(), "owned-job")
	if err := os.Mkdir(job, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(job, "public-fixture.txt")
	if err := os.WriteFile(file, []byte("public fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(syscall.StringToUTF16Ptr(file), syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal("could not acquire native fixture reader")
	}
	closed := false
	defer func() {
		if !closed {
			_ = syscall.CloseHandle(handle)
		}
	}()
	attempts := 0
	// The first actual RemoveAll observes a real no-delete-sharing handle. It
	// is released only on entering the retry wait, so scheduler speed cannot
	// accidentally turn this into a no-contention success.
	err = removeSourceAcceptanceJob(job, func(path string) error { attempts++; return os.RemoveAll(path) }, func(delay time.Duration) {
		if !closed {
			_ = syscall.CloseHandle(handle)
			closed = true
		}
		time.Sleep(delay)
	})
	if err != nil {
		t.Fatal("transient native reader failed otherwise valid workspace cleanup")
	}
	if attempts < 2 {
		t.Fatal("native file reader did not contend with cleanup")
	}
	if _, err := os.Lstat(job); !os.IsNotExist(err) {
		t.Fatal("owned workspace was not removed")
	}
}

func TestSourceAcceptanceCleanupPersistentNativeDenialIsBounded(t *testing.T) {
	for _, code := range []syscall.Errno{5, 32} {
		calls, waits := 0, 0
		var waited time.Duration
		expected := &os.PathError{Op: "remove", Path: "owned-fixture", Err: code}
		actual := removeSourceAcceptanceJob("owned-fixture", func(path string) error {
			if path != "owned-fixture" {
				t.Fatal("cleanup target changed")
			}
			calls++
			return expected
		}, func(delay time.Duration) { waits++; waited += delay })
		if actual != expected || calls != 6 || waits != 5 || waited != 1550*time.Millisecond {
			t.Fatal("persistent denial was not bounded or changed cleanup target")
		}
	}
}

func TestSourceAcceptanceCleanupDoesNotRetryInvalidDirectory(t *testing.T) {
	calls := 0
	expected := &os.PathError{Op: "remove", Path: "owned-fixture", Err: syscall.Errno(267)}
	actual := removeSourceAcceptanceJob("owned-fixture", func(string) error { calls++; return expected }, func(time.Duration) { t.Fatal("unrelated error was retried") })
	if actual != expected || calls != 1 {
		t.Fatal("unrelated error was hidden")
	}
}

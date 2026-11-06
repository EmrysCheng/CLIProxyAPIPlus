package sourceupdate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"

	"cliproxyapi-plus/internal/private"
)

func sourceAcceptanceJob(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return t.TempDir()
	}
	// Go's compiler uses each dependency directory as CreateProcess cwd. Keep
	// the test-owned root short without moving real installation/update paths.
	job, err := os.MkdirTemp("", "cpa-src-")
	if err != nil {
		t.Fatal("could not create source acceptance workspace")
	}
	if err := private.Protect(job, true); err != nil {
		_ = os.Remove(job)
		t.Fatal("could not protect source acceptance workspace")
	}
	t.Cleanup(func() {
		if err := removeSourceAcceptanceJob(job, os.RemoveAll, time.Sleep); err != nil {
			t.Error("could not clean source acceptance workspace")
		}
	})
	return job
}

// Model the actual fixed prepared-source layout, not an arbitrary missing cwd.
// The original acceptance test name plus nested source jobs can exceed Win32's
// CreateProcess current-directory limit before any dependency test runs.
func TestNativeSourceAcceptanceWorkspaceSupportsDeepPreparedDependencyCWD(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Win32 process-directory semantics")
	}
	job := sourceAcceptanceJob(t)
	directory := filepath.Join(job, "workspace", ".build", "candidates", strings.Repeat("a", 32), "source", ".build", "sources", "cpa-manager-plus-"+strings.Repeat("b", 16), "apps", "manager-server", "internal", "http", "controller", "codexinspection")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal("could not create prepared dependency fixture")
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatal("prepared dependency fixture is not present")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^$")
	command.Dir = directory
	if _, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native prepared dependency cwd failed (winerror267=%t), path_utf16_units=%d", errors.Is(err, syscall.Errno(267)), len(utf16.Encode([]rune(directory))))
	}
}

func removeSourceAcceptanceJob(job string, remove func(string) error, sleep func(time.Duration)) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = remove(job)
		if err == nil {
			return nil
		}
		if runtime.GOOS != "windows" || (!errors.Is(err, syscall.Errno(5)) && !errors.Is(err, syscall.Errno(32))) || attempt == 5 {
			return err
		}
		sleep((50 * time.Millisecond) << attempt)
	}
	return err
}

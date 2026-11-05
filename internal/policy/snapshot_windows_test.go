//go:build windows

package policy

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	coreembedded "github.com/router-for-me/CLIProxyAPI/v8/sdk/embedded"
)

func TestKVStoreSnapshotRejectsWindowsJunction(t *testing.T) {
	// A junction is reported as ModeIrregular by Go on Windows. It does not
	// require the symbolic-link privilege, so test it independently of Symlink.
	systemRoot := os.Getenv("SystemRoot")
	if !filepath.IsAbs(systemRoot) {
		t.Skip("Windows system root unavailable")
	}
	powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powershell); err != nil {
		t.Skipf("Windows PowerShell unavailable: %v", err)
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	outside := filepath.Join(root, "outside")
	for _, dir := range []string{data, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(data, "linked")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	script := "New-Item -ItemType Junction -Path " + quote(link) + " -Target " + quote(outside) + " -ErrorAction Stop | Out-Null"
	cmd := exec.Command(powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create private test junction: %v: %s", err, output)
	}
	for _, snapshotRoot := range []string{data, link} {
		err := validateSnapshotKVStore(testKVPath(filepath.Join(link, "kv.db")), snapshotRoot)
		if err == nil || !strings.Contains(err.Error(), "symbolic links/junctions") {
			t.Fatalf("junction not rejected: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "kv.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation opened external database", err)
	}
}

func TestKVStoreSnapshotRejectsWindowsNamedStreams(t *testing.T) {
	data := t.TempDir()
	t.Chdir(data)
	for _, path := range []string{
		filepath.Join(data, "carrier:kv.db"), "carrier:kv.db",
		"carrier:kv.db:$DATA", "carrier::$DATA", "carrier:",
		filepath.Join(data, "parent:stream", "kv.db"),
		"  carrier:kv.db  ",
	} {
		t.Run(path, func(t *testing.T) {
			err := validateSnapshotKVStore(testKVPath(path), data)
			if err == nil || !strings.Contains(err.Error(), "named streams") {
				t.Fatalf("stream syntax not rejected before filesystem access: %v", err)
			}
		})
	}
	// The drive-letter colon is a volume prefix, not a named stream.
	for _, path := range []string{filepath.Join(data, "ordinary.db"), "ordinary.db"} {
		if err := validateSnapshotKVStore(testKVPath(path), data); err != nil {
			t.Fatalf("ordinary path rejected: %v", err)
		}
	}

	volume := filepath.VolumeName(data)
	if len(volume) == 2 && strings.HasSuffix(volume, ":") {
		if err := validateSnapshotKVStore(testKVPath(volume+"ordinary.db"), data); err != nil {
			t.Fatalf("drive-relative ordinary path rejected: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(data, "carrier")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation created a stream carrier", err)
	}
}

func TestNativeKVStoreSnapshotRejectsWindowsNamedStreams(t *testing.T) {
	if _, ok := any(&config.Config{}).(kvStorePathProvider); !ok {
		t.Skip("pinned core predates the optional KVStore API")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data")
	auth := filepath.Join(data, "auths")
	if err := os.MkdirAll(auth, 0700); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	raw := fmt.Sprintf("host: 127.0.0.1\nport: 8317\nauth-dir: %q\nremote-management:\n  secret-key: %s\n  disable-control-panel: true\n  disable-auto-update-panel: true\n", filepath.ToSlash(auth), key)
	base, err := config.ParseConfigBytes([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	raw = strings.Replace(raw, key, base.RemoteManagement.SecretKey, 1)
	boundary := WithSnapshotKVStoreBoundary(Frozen(base, key, root), data)
	bad := raw + fmt.Sprintf("kv-store:\n  path: %q\n", filepath.ToSlash(filepath.Join(data, "carrier:kv.db")))
	next, err := config.ParseConfigBytes([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	if err = boundary(next); err == nil || !strings.Contains(err.Error(), "named streams") {
		t.Fatal("startup policy accepted stream", err)
	}
	release, err := coreembedded.InstallConfigPolicy(boundary)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, disabled := range []bool{false, true} {
		payload := raw + fmt.Sprintf("kv-store:\n  disabled: %v\n  path: %q\n", disabled, filepath.ToSlash(filepath.Join(data, "carrier:kv.db")))
		if _, err = config.ParseConfigBytes([]byte(payload)); err == nil || !strings.Contains(err.Error(), "named streams") {
			t.Fatal("parser/reload accepted stream", err)
		}
	}
	file := filepath.Join(root, "config.yaml")
	if err = os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err = config.SaveConfigPreserveComments(file, next); err == nil || !strings.Contains(err.Error(), "named streams") {
		t.Fatal("save accepted stream", err)
	}
	after, _ := os.ReadFile(file)
	if string(after) != raw {
		t.Fatal("rejected save changed configuration")
	}
	if err = os.WriteFile(file, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = config.LoadConfig(file); err == nil || !strings.Contains(err.Error(), "named streams") {
		t.Fatal("disk reload accepted stream", err)
	}
	if _, err = os.Stat(filepath.Join(data, "carrier")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("policy created a stream carrier", err)
	}
}

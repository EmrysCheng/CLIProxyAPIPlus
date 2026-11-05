package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	coreembedded "github.com/router-for-me/CLIProxyAPI/v8/sdk/embedded"
)

type testKVPath string

func (p testKVPath) EffectiveKVStorePath() string { return string(p) }

func TestKVStoreSnapshotPaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(data, "auths", "kv.db")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, good)
	cases := []struct {
		name, path string
		allowed    bool
	}{
		{"default-shaped", good, true},
		{"other-contained-file", filepath.Join(data, "other.db"), true},
		{"outside", filepath.Join(root, "external.db"), false},
		{"sibling-prefix", filepath.Join(root, "data-other", "kv.db"), false},
		{"parent-traversal", filepath.Join(data, "..", "external.db"), false},
		{"directory-not-file", data, false},
		{"empty", "", false},
	}
	if err == nil {
		cases = append(cases, struct {
			name, path string
			allowed    bool
		}{"cwd-relative", relative, true})
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			err := validateSnapshotKVStore(testKVPath(item.path), data)
			if (err == nil) != item.allowed {
				t.Fatalf("allowed=%v err=%v", item.allowed, err)
			}
		})
	}
	if err := validateSnapshotKVStore(struct{}{}, data); err != nil {
		t.Fatal("legacy core rejected:", err)
	}
	if _, err := os.Stat(good); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation created a database", err)
	}
	directory := filepath.Join(data, "directory.db")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotKVStore(testKVPath(directory), data); err == nil {
		t.Fatal("directory accepted as KV file")
	}
}

func TestKVStoreSnapshotRejectsLinkAncestors(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	external := filepath.Join(root, "external")
	for _, path := range []string{data, external} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(data, "linked")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := validateSnapshotKVStore(testKVPath(filepath.Join(link, "kv.db")), data); err == nil {
		t.Fatal("symlink ancestor escaped snapshot")
	}
	if _, err := os.Stat(filepath.Join(external, "kv.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation touched external file", err)
	}
}

func TestKVStoreSnapshotPreservesExistingPolicy(t *testing.T) {
	expected := errors.New("original policy rejected")
	calls := 0
	wrapped := WithSnapshotKVStoreBoundary(func(*config.Config) error { calls++; return expected }, t.TempDir())
	if err := wrapped(&config.Config{}); !errors.Is(err, expected) || calls != 1 {
		t.Fatal("original policy bypassed", err, calls)
	}
	if err := wrapped(nil); err == nil {
		t.Fatal("nil configuration accepted")
	}
}

func TestNativeKVStoreSnapshotParseLoadSaveBoundary(t *testing.T) {
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
	boundary := WithSnapshotKVStoreBoundary(Frozen(base, key, root), data)
	if err = boundary(base); err != nil {
		t.Fatal("default path rejected", err)
	}
	t.Chdir(root)
	relativeBase := *base
	relativeBase.AuthDir = filepath.Join("data", "auths")
	if err = boundary(&relativeBase); err != nil {
		t.Fatal("relative default auth path rejected", err)
	}
	if relativeBase.AuthDir != filepath.Join("data", "auths") {
		t.Fatal("validation mutated caller")
	}
	other := t.TempDir()
	t.Chdir(other)
	if err = boundary(&relativeBase); err == nil {
		t.Fatal("relative effective path outside data accepted when cwd differs from config directory")
	}
	t.Chdir(root)

	outside := filepath.Join(root, "external.db")
	bad := raw + fmt.Sprintf("kv-store:\n  disabled: false\n  path: %q\n", filepath.ToSlash(outside))
	next, err := config.ParseConfigBytes([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	if err = boundary(next); err == nil {
		t.Fatal("external startup path accepted")
	}
	disabled := strings.Replace(bad, "disabled: false", "disabled: true", 1)
	off, err := config.ParseConfigBytes([]byte(disabled))
	if err != nil {
		t.Fatal(err)
	}
	if err = boundary(off); err == nil {
		t.Fatal("disabled external path preconfiguration accepted")
	}
	release, err := coreembedded.InstallConfigPolicy(boundary)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = config.ParseConfigBytes([]byte(bad)); err == nil {
		t.Fatal("parser/reload bypassed boundary")
	}
	t.Chdir(data)
	for _, path := range []string{"~", "~/kv.db", `~\kv.db`, "~other/kv.db", "  ~/kv.db  "} {
		homeConfig := raw + fmt.Sprintf("kv-store:\n  path: %q\n", path)
		if _, err = config.ParseConfigBytes([]byte(homeConfig)); err == nil {
			t.Fatalf("native parser accepted home expansion from data cwd: %q", path)
		}
	}
	t.Chdir(root)

	file := filepath.Join(root, "config.yaml")
	if err = os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err = config.SaveConfigPreserveComments(file, next); err == nil {
		t.Fatal("save bypassed boundary")
	}
	after, _ := os.ReadFile(file)
	if string(after) != raw {
		t.Fatal("rejected save changed configuration")
	}
	if err = os.WriteFile(file, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = config.LoadConfig(file); err == nil {
		t.Fatal("disk reload bypassed boundary")
	}
	contained := raw + fmt.Sprintf("kv-store:\n  path: %q\n", filepath.ToSlash(filepath.Join(data, "other.db")))
	if _, err = config.ParseConfigBytes([]byte(contained)); err != nil {
		t.Fatal("contained path rejected", err)
	}
	if _, err = os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("policy opened external database", err)
	}
}

func TestKVStoreSnapshotRejectsHomeExpansionForms(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(data)
	// Match the upstream home-expansion cases without ever opening the home DB.
	for _, path := range []string{"~", "~/kv.db", `~\kv.db`, "~other/kv.db", "  ~/kv.db  "} {
		if err := validateSnapshotKVStore(testKVPath(path), data); err == nil {
			t.Fatalf("home expansion accepted as an internal relative path: %q", path)
		}
	}
	ancestor := filepath.Join(data, "file-parent")
	if err := os.WriteFile(ancestor, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotKVStore(testKVPath(filepath.Join(ancestor, "kv.db")), data); err == nil {
		t.Fatal("regular file ancestor accepted")
	}
}

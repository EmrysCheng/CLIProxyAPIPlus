package policy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Older supported cores have no KVStore API. Detect the newer capability without
// changing their config layout or using reflection to mutate upstream settings.
type kvStorePathProvider interface{ EffectiveKVStorePath() string }

// WithSnapshotKVStoreBoundary keeps every possible KV database inside the data
// tree already covered by maintenance snapshots. The caller uses this same
// policy at startup and for parse/load/save/reload before the service applies it.
// Even a disabled store may not preconfigure an external path in web-update mode.
func WithSnapshotKVStoreBoundary(base func(*config.Config) error, dataDir string) func(*config.Config) error {
	return func(cfg *config.Config) error {
		if cfg == nil || base == nil {
			return errors.New("embedded configuration policy is required")
		}
		if err := base(cfg); err != nil {
			return err
		}
		return validateSnapshotKVStore(cfg, dataDir)
	}
}

func validateSnapshotKVStore(cfg any, dataDir string) error {
	provider, ok := cfg.(kvStorePathProvider)
	if !ok {
		return nil
	}
	path := strings.TrimSpace(provider.EffectiveKVStorePath())
	if path == "" || dataDir == "" {
		return errors.New("web updates require a valid KVStore data path")
	}
	// The upstream store expands every leading ~ before opening. Do not treat
	// it as a literal relative directory under Data; require a literal path.
	if strings.HasPrefix(path, "~") {
		return errors.New("web updates require literal KVStore paths; replace ~ with an absolute path inside data-dir")
	}
	// Named streams are not separately enumerated or copied by data snapshots.
	// Reject their syntax before touching the filesystem, but keep drive colons.
	if windowsNamedStreamPath(path) {
		return errors.New("web updates forbid Windows named streams in KVStore paths")
	}
	// Upstream opens an explicit relative path against the process working
	// directory, not the configuration directory. Check the actual effective
	// path without normalizing a copy that the service would never receive.
	path, err := filepath.Abs(path)
	if err != nil {
		return errors.New("cannot resolve KVStore path for web updates")
	}
	if windowsNamedStreamPath(path) {
		return errors.New("web updates forbid Windows named streams in KVStore paths")
	}
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return errors.New("cannot resolve data directory for web updates")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("web updates require kv-store.path inside data-dir; use offline maintenance for external KV stores")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return errors.New("cannot verify KVStore path for web updates")
		}
		if statErr == nil {
			if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
				return errors.New("web updates forbid symbolic links/junctions in KVStore paths")
			}
			if current == path && !info.Mode().IsRegular() {
				return errors.New("web updates require KVStore path to be a regular file")
			}
			if current != path && !info.IsDir() {
				return errors.New("web updates require directory-only KVStore ancestors")
			}
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func windowsNamedStreamPath(path string) bool {
	return runtime.GOOS == "windows" && strings.Contains(strings.TrimPrefix(path, filepath.VolumeName(path)), ":")
}

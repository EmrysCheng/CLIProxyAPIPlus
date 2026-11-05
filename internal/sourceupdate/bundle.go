package sourceupdate

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed toolchains.lock.json
var toolchainLock []byte

type toolchainManifest struct {
	Schema      int               `json:"schema_version"`
	Target      string            `json:"target"`
	Tools       Tools             `json:"tools"`
	Records     map[string]string `json:"records"`
	Executables []string          `json:"executables"`
}

func bundledTools(ctx context.Context, directory string) (Tools, error) {
	var lock struct {
		Schema    int `json:"schema_version"`
		Platforms map[string]struct {
			Manifest string `json:"manifest_sha256"`
		} `json:"platforms"`
	}
	if json.Unmarshal(toolchainLock, &lock) != nil || lock.Schema != 1 {
		return Tools{}, errors.New("invalid embedded toolchain lock")
	}
	target := runtime.GOOS + "/" + runtime.GOARCH
	return verifyToolBundle(ctx, directory, target, lock.Platforms[target].Manifest)
}

// A bundle is pinned by the binary, not by a mutable adjacent checksum file.
// Verify before executing any SDK code; never fall back to PATH on corruption.
func verifyToolBundle(ctx context.Context, directory, target, expected string) (Tools, error) {
	var result Tools
	if !sourceHashPattern.MatchString(expected) {
		return result, errors.New("unsupported bundled toolchain platform")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("bundled toolchain directory missing or linked")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	// Walk without following links, including Windows reparse points.
	names := map[string]bool{}
	if err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if name == "." {
			return nil
		}
		if !safeKitName(name) || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unsafe bundled toolchain path or link")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("special file in bundled toolchain")
		}
		names[name] = true
		if len(names) > 40000 {
			return errors.New("toolchain file count limit")
		}
		return nil
	}); err != nil {
		return result, err
	}
	manifestFile, err := root.Open("toolchain-manifest.json")
	if err != nil {
		return result, err
	}
	raw, err := io.ReadAll(io.LimitReader(manifestFile, (8<<20)+1))
	_ = manifestFile.Close()
	sum := sha256.Sum256(raw)
	if err != nil || len(raw) > 8<<20 || hex.EncodeToString(sum[:]) != expected {
		return result, errors.New("bundled toolchain manifest checksum mismatch")
	}
	var manifest toolchainManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != 1 || manifest.Target != target || len(manifest.Records) == 0 || len(manifest.Records)+1 != len(names) {
		return result, errors.New("bundled toolchain platform or inventory mismatch")
	}
	total := int64(0)
	for name, expectedHash := range manifest.Records {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if !safeKitName(name) || !names[name] || !sourceHashPattern.MatchString(expectedHash) {
			return result, errors.New("invalid bundled toolchain inventory entry")
		}
		file, err := root.Open(name)
		if err != nil {
			return result, err
		}
		hash := sha256.New()
		size, err := io.Copy(hash, io.LimitReader(file, (1<<30)+1))
		_ = file.Close()
		total += size
		if err != nil || total > 1<<30 || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
			return result, errors.New("bundled toolchain file checksum mismatch")
		}
	}
	if runtime.GOOS != "windows" {
		for _, name := range manifest.Executables {
			if !names[name] || !safeKitName(name) {
				return result, errors.New("invalid tool executable")
			}
			info, err := root.Stat(name)
			if err != nil || info.Mode()&0111 == 0 {
				return result, errors.New("toolchain executable permission missing")
			}
		}
	}
	resolve := func(name string) string {
		if !safeKitName(name) || manifest.Records[name] == "" {
			return ""
		}
		return filepath.Join(directory, filepath.FromSlash(name))
	}
	result = Tools{Bundle: directory, Python: resolve(manifest.Tools.Python), Go: resolve(manifest.Tools.Go), Node: resolve(manifest.Tools.Node), NpmCLI: resolve(manifest.Tools.NpmCLI)}
	if result.Python == "" || result.Go == "" || result.Node == "" || result.NpmCLI == "" {
		return Tools{}, errors.New("bundled SDK entry point missing")
	}
	return result, nil
}

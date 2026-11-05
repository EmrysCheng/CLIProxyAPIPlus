package webupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cliproxyapi-plus/internal/private"
)

func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func noLinks(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links/junctions are not supported: %s", p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func privateDirectory(path string) error {
	if err := noLinks(path); err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if err := trustedOwner(path); err != nil {
		return err
	}
	return private.Protect(path, true)
}
func fileHash(path string) (string, error) {
	if err := noLinks(path); err != nil {
		return "", err
	}
	f, err := openRead(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("expected a regular file")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func readJSON(path string, target any) error {
	if err := noLinks(path); err != nil {
		return err
	}
	if err := trustedOwner(path); err != nil {
		return err
	}
	file, err := openRead(path)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, 8<<20+1))
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("update record exceeds limit")
	}
	return json.Unmarshal(data, target)
}
func writeJSON(path string, value any) error {
	bytes, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(bytes) >= 8<<20 {
		return errors.New("update record exceeds the readable size limit")
	}
	return atomicFile(path, append(bytes, '\n'), 0600)
}
func atomicFile(path string, data []byte, mode os.FileMode) error {
	if err := noLinks(path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = private.Protect(name, false); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return err
	}
	return renameFile(name, path)
}
func copyFile(source, destination string, limit int64, mode os.FileMode) error {
	if err := noLinks(source); err != nil {
		return err
	}
	if err := noLinks(destination); err != nil {
		return err
	}
	in, err := openRead(source)
	if err != nil {
		return err
	}
	defer in.Close()
	stat, err := in.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > limit {
		return errors.New("invalid source file or size limit exceeded")
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".copy-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = private.Protect(name, false); err == nil {
		_, err = io.Copy(tmp, io.LimitReader(in, limit+1))
	}
	if err == nil {
		var info os.FileInfo
		info, err = tmp.Stat()
		if err == nil && info.Size() > limit {
			err = errors.New("copy size limit exceeded")
		}
	}
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = tmp.Sync()
	}
	err = errors.Join(err, tmp.Close())
	if err != nil {
		return err
	}
	return renameFile(name, destination)
}
func validateInstallation(o Options) error {
	for _, path := range []string{o.Binary, o.Config, o.Data} {
		if !filepath.IsAbs(path) {
			return errors.New("installation paths must be absolute")
		}
		if err := noLinks(path); err != nil {
			return err
		}
	}
	if inside(o.Data, o.Binary) || inside(o.Data, o.stateDir()) || inside(o.stateDir(), o.Config) || inside(o.stateDir(), o.Binary) {
		return errors.New("installation, data and update state paths overlap")
	}
	if o.Auth == "" || !inside(o.Data, o.Auth) {
		return errors.New("web updates require auth-dir inside data-dir; use offline maintenance for external credentials")
	}
	for _, path := range []string{o.TLSCert, o.TLSKey} {
		if path != "" && !inside(o.Data, path) {
			return errors.New("web updates require TLS material inside data-dir")
		}
	}
	for _, path := range []string{o.Binary, o.Config} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("current binary and configuration must exist")
		}
	}
	if info, err := os.Stat(o.Data); err != nil || !info.IsDir() {
		return errors.New("data directory does not exist")
	}
	u, err := url.Parse(o.HealthURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (net.ParseIP(u.Hostname()) == nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "") {
		return errors.New("health endpoint must be a literal listener-IP URL without credentials or paths")
	}
	// Test directory write/delete access before asking the running instance to
	// stop. Never attempt to replace a live executable as a permission probe.
	seen := map[string]bool{}
	for _, dir := range []string{filepath.Dir(o.Binary), filepath.Dir(o.Config), filepath.Dir(o.Data)} {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		probe, err := os.CreateTemp(dir, ".web-update-access-*")
		if err != nil {
			return errors.New("installation/config/data parent directory is not writable")
		}
		name := probe.Name()
		err = probe.Close()
		removeErr := os.Remove(name)
		if err != nil || removeErr != nil {
			return errors.New("installation directory cannot safely stage and remove files")
		}
	}
	return nil
}
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") && strings.EqualFold(u.Host, r.Host)
}
func sortHistory(history []map[string]any) {
	sort.Slice(history, func(i, j int) bool { return history[i]["created_at"].(string) > history[j]["created_at"].(string) })
}

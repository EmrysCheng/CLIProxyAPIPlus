package releaseserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"cliproxyapi-plus/internal/product"
)

const MaxPackageBytes = int64(512 << 20)

var packageName = regexp.MustCompile(`^CLIProxyAPIPlus_[0-9A-Za-z.+-]+_(windows|linux)_(amd64|arm64)_portable\.(zip|tar\.gz)$`)

type Handler struct {
	root   *os.Root
	origin string
	host   string
	logger *log.Logger
}

func NewHandler(dir, address string, logger *log.Logger) (*Handler, error) {
	if err := ValidateListen(address); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	h := &Handler{root: root, origin: "http://" + address, host: address, logger: logger}
	_, index, err := h.readIndex()
	if err == nil {
		// Refuse a broken prepared bundle before binding a port. Later publications
		// remain client SHA256-verified and should atomically replace the index last.
		for _, asset := range index.Release.Assets {
			file, e := h.openPackage(asset.Name)
			if e != nil {
				err = e
				break
			}
			sum := sha256.New()
			_, e = io.Copy(sum, io.LimitReader(file, MaxPackageBytes+1))
			file.Close()
			if e != nil || !strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), asset.SHA256) {
				err = fmt.Errorf("package checksum mismatch: %s", asset.Name)
				break
			}
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return h, nil
}
func (h *Handler) Close() error { return h.root.Close() }

func (h *Handler) readIndex() ([]byte, product.Index, error) {
	var index product.Index
	info, err := h.root.Lstat("index.json")
	if err != nil {
		return nil, index, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, index, errors.New("index.json must be a regular JSON file at most 1 MiB")
	}
	file, err := h.root.Open("index.json")
	if err != nil {
		return nil, index, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	file.Close()
	if err != nil || len(raw) > 1<<20 {
		return nil, index, errors.New("index read failed or exceeds limit")
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, index, err
	}
	if err := product.ValidateReleaseIndex(index); err != nil {
		return nil, index, err
	}
	if len(index.Release.Assets) == 0 || len(index.Release.Assets) > 64 {
		return nil, index, errors.New("index must contain 1..64 Portable packages")
	}
	policy := product.ReleaseSourcePolicy{AllowLocalHTTP: true}
	seen := map[string]bool{}
	for _, asset := range index.Release.Assets {
		target, e := url.Parse(asset.URL)
		if !packageName.MatchString(asset.Name) || seen[asset.Name] || e != nil || !policy.AllowsAsset(h.origin+"/index.json", asset.URL) || target.Path != "/"+asset.Name || target.RawQuery != "" {
			return nil, index, errors.New("index packages must have unique integrated Portable names and use this server's exact HTTP origin and root path")
		}
		seen[asset.Name] = true
	}
	return raw, index, nil
}

func (h *Handler) openPackage(name string) (*os.File, error) {
	if !packageName.MatchString(name) {
		return nil, os.ErrNotExist
	}
	info, err := h.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxPackageBytes {
		return nil, errors.New("package must be a regular file, no links, at most 512 MiB")
	}
	file, err := h.root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() > MaxPackageBytes {
		file.Close()
		return nil, errors.New("package changed or is not a bounded regular file")
	}
	return file, nil
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int)        { w.status = code; w.ResponseWriter.WriteHeader(code) }
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	capture := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	w = capture
	status := http.StatusOK
	if h.logger != nil {
		start := time.Now()
		defer func() {
			path := r.URL.Path
			if len(path) > 256 {
				path = path[:256]
			}
			h.logger.Printf("%s %q status=%d duration=%s", r.Method, path, capture.status, time.Since(start).Round(time.Millisecond))
		}()
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !strings.EqualFold(r.Host, h.host) {
		status = 403
		http.Error(w, "host not allowed", status)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		status = 405
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read-only release service", status)
		return
	}
	if r.URL.RawQuery != "" {
		status = 404
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != "/index.json" && r.URL.Path != "/healthz" && !packageName.MatchString(strings.TrimPrefix(r.URL.Path, "/")) {
		status = 404
		http.NotFound(w, r)
		return
	}
	raw, index, err := h.readIndex()
	if err != nil {
		status = 503
		if h.logger != nil {
			h.logger.Printf("release index unavailable: %v", err)
		}
		http.Error(w, "release index unavailable; check server log", status)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
		}
		return
	case "/index.json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		http.ServeContent(w, r, "index.json", time.Time{}, bytes.NewReader(raw))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	for _, asset := range index.Release.Assets {
		if asset.Name != name {
			continue
		}
		file, err := h.openPackage(name)
		if err != nil {
			status = 404
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			status = 503
			http.Error(w, "package unavailable", status)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", `"`+strings.ToLower(asset.SHA256)+`"`)
		http.ServeContent(w, r, name, info.ModTime(), file)
		return
	}
	status = 404
	http.NotFound(w, r)
}

// Package integration provides the single-port boundary between CPA and Manager.
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

type handlerRef struct{ handler http.Handler }
type internalKey struct{}
type authRef struct {
	verify func(context.Context, string) (bool, error)
}

// Bridge is installed before the upstream routes and cannot be bypassed by HTTP headers.
type Bridge struct {
	core      atomic.Pointer[handlerRef]
	manager   atomic.Pointer[handlerRef]
	coreReady atomic.Bool
	Version   any
	Product   http.Handler
	auth      atomic.Pointer[authRef]
	stopMu    sync.Mutex
	stopping  bool
	active    int
	drained   chan struct{}
}

func (b *Bridge) SetAuthorizer(verify func(context.Context, string) (bool, error)) {
	b.auth.Store(&authRef{verify})
}
func (b *Bridge) enter() bool {
	b.stopMu.Lock()
	defer b.stopMu.Unlock()
	if b.stopping {
		return false
	}
	b.active++
	return true
}
func (b *Bridge) leave() {
	b.stopMu.Lock()
	defer b.stopMu.Unlock()
	b.active--
	if b.stopping && b.active == 0 {
		close(b.drained)
	}
}
func (b *Bridge) StopAccepting() {
	b.stopMu.Lock()
	defer b.stopMu.Unlock()
	if !b.stopping {
		b.stopping = true
		b.drained = make(chan struct{})
		if b.active == 0 {
			close(b.drained)
		}
	}
}
func (b *Bridge) Drain(ctx context.Context) error {
	b.StopAccepting()
	b.stopMu.Lock()
	done := b.drained
	b.stopMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (b *Bridge) serveProduct(w http.ResponseWriter, r *http.Request) {
	auth := b.auth.Load()
	if auth == nil || b.manager.Load() == nil {
		writeJSON(w, 503, gin.H{"error": "Manager is not ready"})
		return
	}
	ok, err := auth.verify(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		writeJSON(w, 503, gin.H{"error": "admin verification unavailable"})
		return
	}
	if !ok {
		writeJSON(w, 401, gin.H{"error": "invalid admin key"})
		return
	}
	if b.Product == nil {
		writeJSON(w, 503, gin.H{"error": "product release service unavailable"})
		return
	}
	b.Product.ServeHTTP(w, r)
}

func (b *Bridge) SetCore(handler http.Handler) { b.core.Store(&handlerRef{handler}) }
func (b *Bridge) SetManager(handler http.Handler) {
	if handler == nil {
		b.manager.Store(nil)
		return
	}
	b.manager.Store(&handlerRef{handler})
}
func (b *Bridge) MarkCoreReady(ready bool) { b.coreReady.Store(ready) }
func (b *Bridge) Ready() bool              { return b.coreReady.Load() && b.manager.Load() != nil }

func boundary(path, prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }

func canonical(u *url.URL) bool {
	if u == nil || u.RawPath != "" || strings.Contains(u.Path, "//") || strings.Contains(u.Path, "\\") {
		return false
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func managerPath(path string) bool {
	switch path {
	case "/", "/management.html", "/favicon.ico", "/apple-touch-icon.png", "/setup", "/status", "/health":
		return true
	}
	return boundary(path, "/usage-service") || boundary(path, "/v0/management") ||
		boundary(path, "/v8/management") || boundary(path, "/v0/resource/plugins")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (b *Bridge) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Context().Value(internalKey{}) == b {
			c.Next()
			return
		}
		if !b.enter() {
			c.Writer.Header().Set("Retry-After", "2")
			writeJSON(c.Writer, 503, gin.H{"error": "server is restarting for update; please retry in a moment"})
			c.Abort()
			return
		}
		defer b.leave()
		if !canonical(c.Request.URL) {
			writeJSON(c.Writer, http.StatusBadRequest, gin.H{"error": "non-canonical request path"})
			c.Abort()
			return
		}
		path := c.Request.URL.Path
		if boundary(path, "/plus-api") {
			c.Abort()
			if boundary(path, "/plus-api/releases") || path == "/plus-api/runtime" {
				c.Status(http.StatusOK)
				b.serveProduct(c.Writer, c.Request)
				return
			}
			if c.Request.Method != http.MethodGet {
				writeJSON(c.Writer, 405, gin.H{"error": "method not allowed"})
				return
			}
			switch path {
			case "/plus-api/ready":
				if !b.Ready() {
					writeJSON(c.Writer, 503, gin.H{"ready": false})
					return
				}
				writeJSON(c.Writer, 200, gin.H{"ready": true})
			case "/plus-api/version":
				writeJSON(c.Writer, 200, b.Version)
			default:
				writeJSON(c.Writer, 404, gin.H{"error": "not found"})
			}
			return
		}
		// This is an upstream public callback, not a general v8 management bypass.
		if path == "/v8/management/oauth/callback" && (c.Request.Method == http.MethodGet || c.Request.Method == http.MethodPost) {
			c.Next()
			return
		}
		if !managerPath(path) {
			c.Next()
			return
		}
		c.Abort()
		if boundary(path, "/v8/management") && !boundary(path, "/v8/management/config/api-keys") {
			writeJSON(c.Writer, 404, gin.H{"error": "v8 management route is not approved for embedded Manager"})
			return
		}
		if boundary(path, "/usage-service/updates") {
			c.Status(http.StatusOK)
			b.serveProduct(c.Writer, c.Request)
			return
		}
		manager := b.manager.Load()
		if manager == nil {
			writeJSON(c.Writer, 503, gin.H{"error": "embedded Manager is not ready"})
			return
		}
		// Gin preselects 404 on its NoRoute chain. Manager handlers expect the
		// net/http default of 200 when they write a body without WriteHeader.
		c.Status(http.StatusOK)
		manager.handler.ServeHTTP(c.Writer, c.Request)
	}
}

func (b *Bridge) coreRequest(req *http.Request) (*http.Request, http.Handler) {
	core := b.core.Load()
	if core == nil {
		return nil, nil
	}
	req = req.Clone(context.WithValue(req.Context(), internalKey{}, b))
	// The transport is trusted in-process, but upstream credential checks still run.
	req.RemoteAddr = "127.0.0.1:0"
	req.RequestURI = ""
	return req, core.handler
}

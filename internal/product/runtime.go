package product

import (
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Handler is protected by the Manager DB-backed authorizer in Bridge. It never
// exposes configuration, credentials, process environment, or runtime profiles.
func Handler(releases *Releases, updates ...http.Handler) http.Handler {
	started := time.Now()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/plus-api/releases/upstreams") && len(updates) > 1 {
			updates[1].ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/plus-api/releases/operations") && len(updates) > 0 {
			updates[0].ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/plus-api/runtime" {
			releases.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		w.WriteHeader(200)
		_ = json.NewEncoder(w).Encode(map[string]any{"uptime_seconds": time.Since(started).Seconds(), "goroutines": runtime.NumGoroutine(), "heap_alloc_bytes": memory.HeapAlloc, "heap_sys_bytes": memory.HeapSys, "gc_cycles": memory.NumGC})
	})
}

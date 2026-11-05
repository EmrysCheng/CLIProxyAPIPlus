package embedded

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"time"
)

// RunStandalone shares initialization and drain ownership with embedded mode.
// Listener ownership stays on this goroutine; startup cancellation cannot race
// callback publication or create a listener after Shutdown has completed.
func RunStandalone(parent context.Context, cfg Config) error {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	runtimeDone := make(chan error, 1)
	ready := make(chan http.Handler)
	bound := make(chan struct{})
	serveDone := make(chan error, 2)
	go func() {
		runtimeDone <- runConfigured(ctx, cfg, nil, func(handler http.Handler) {
			select {
			case ready <- handler:
				select {
				case <-bound:
				case <-ctx.Done():
				}
			case <-ctx.Done():
			}
		}, nil, true)
	}()
	var servers []*http.Server
	var first error
	runtimeExited := false
	start := func(addr string, handler http.Handler) error {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, server)
		log.Printf("cpa-manager-plus listening on %s", listener.Addr())
		go func() {
			err := server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			serveDone <- err
		}()
		return nil
	}
	select {
	case <-parent.Done():
	case first = <-runtimeDone:
		runtimeExited = true
	case handler := <-ready:
		if parent.Err() == nil {
			first = start(cfg.HTTPAddr, handler)
			close(bound)
			if first == nil && cfg.PprofAddr != "" {
				mux := http.NewServeMux()
				mux.HandleFunc("/debug/pprof/", httppprof.Index)
				mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
				mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
				mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
				first = start(cfg.PprofAddr, mux)
			}
			if first == nil {
				select {
				case <-parent.Done():
				case first = <-serveDone:
				case first = <-runtimeDone:
					runtimeExited = true
				}
			}
		}
	}
	shutdown, finish := context.WithTimeout(context.Background(), 30*time.Second)
	defer finish()
	for _, server := range servers {
		err := server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		first = errors.Join(first, err)
	}
	cancel()
	if !runtimeExited {
		select {
		case err := <-runtimeDone:
			first = errors.Join(first, err)
		case <-shutdown.Done():
			first = errors.Join(first, fmt.Errorf("standalone Manager drain: %w", shutdown.Err()))
		}
	}
	return first
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"cliproxyapi-plus/internal/releaseserver"
)

var Version = "0.3.0-rc.3"
var SourceDigest = "development"
var BuildDate = "unknown"

func run(ctx context.Context, args []string, out io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("cliproxyapi-plus-release-server", flag.ContinueOnError)
	fs.SetOutput(out)
	configPath := fs.String("config", filepath.Join(filepath.Dir(executable), "release-server.json"), "JSON config; relative release/log paths resolve from its directory")
	version := fs.Bool("version", false, "Print local release-server build identity")
	check := fs.Bool("check", false, "Validate configuration, index and package checksums without listening")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *version {
		return json.NewEncoder(out).Encode(map[string]any{"role": "local-release-server", "version": Version, "phase": "release-candidate", "product_source_sha256": SourceDigest, "build_date": BuildDate, "os": runtime.GOOS, "arch": runtime.GOARCH})
	}
	config, err := releaseserver.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	logging, err := releaseserver.OpenLog(config.LogFile, int64(config.LogMaxSizeMB)<<20)
	if err != nil {
		return fmt.Errorf("open release server log: %w", err)
	}
	defer logging.Close()
	logger := log.New(logging, "", log.LstdFlags|log.LUTC)
	handler, err := releaseserver.NewHandler(config.ReleaseDir, config.Listen, logger)
	if err != nil {
		return fmt.Errorf("validate release directory: %w", err)
	}
	defer handler.Close()
	if *check {
		fmt.Fprintln(out, "Release configuration, index and package checksums verified.")
		return nil
	}
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, WriteTimeout: 5 * time.Minute, MaxHeaderBytes: 16 << 10, ErrorLog: logger}
	logger.Printf("starting %s on %s; root=%s", Version, config.Listen, config.ReleaseDir)
	fmt.Fprintf(out, "CLIProxyAPIPlus ReleaseServer %s (local read-only)\nIndex: http://%s/index.json\nLog: %s\nPress Ctrl+C to stop.\n", Version, config.Listen, config.LogFile)
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		timeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(timeout); err != nil {
			server.Close()
			return err
		}
		if err := <-stopped; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	logger.Println("release server stopped")
	return nil
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

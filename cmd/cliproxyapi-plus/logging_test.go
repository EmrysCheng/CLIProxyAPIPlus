package main

import (
	"bytes"
	coreembedded "github.com/router-for-me/CLIProxyAPI/v8/sdk/embedded"
	log "github.com/sirupsen/logrus"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestFileLoggingDefaultsAndExplicitConsole(t *testing.T) {
	release := coreembedded.UseProductLoggingDefaults()
	defer release()
	for _, tc := range []struct {
		name, yaml string
		file       bool
		limit      int
	}{
		{"existing-config-without-logging-fields", "host: 127.0.0.1\nport: 8317\n", true, 100},
		{"explicit-console-and-unlimited", "logging-to-file: false\nlogs-max-total-size-mb: 0\n", false, 0},
		{"explicit-file-and-custom-limit", "logging-to-file: true\nlogs-max-total-size-mb: 256\n", true, 256},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			disk, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			upload, err := config.ParseConfigBytes([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}
			for mode, cfg := range map[string]*config.Config{"disk": disk, "upload": upload} {
				if cfg.LoggingToFile != tc.file || cfg.LogsMaxTotalSizeMB != tc.limit {
					t.Errorf("%s logging-to-file=%v logs-max-total-size-mb=%d; want %v/%d", mode, cfg.LoggingToFile, cfg.LogsMaxTotalSizeMB, tc.file, tc.limit)
				}
			}
			after, _ := os.ReadFile(path)
			if string(after) != tc.yaml {
				t.Fatal("loading defaults rewrote existing config")
			}
		})
	}
}

func TestSharedRotatingLogAndDynamicDestination(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	cfg := &config.Config{LoggingToFile: true, LogsMaxTotalSizeMB: 100}
	path, closeLogging, err := coreembedded.ConfigureLogging(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeLogging)
	if path != filepath.Join(dir, "logs", "main.log") {
		t.Fatalf("unexpected path: %s", path)
	}
	log.Info("core-file-marker")
	stdlog.Print("manager-file-marker")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"core-file-marker", "manager-file-marker"} {
		if !strings.Contains(string(content), marker) {
			t.Fatalf("missing %s: %s", marker, content)
		}
	}
	// Native config reload changes the shared logger destination. Manager must
	// resolve that destination dynamically rather than retain the original file.
	var console bytes.Buffer
	log.SetOutput(&console)
	stdlog.Print("manager-reload-marker")
	if !strings.Contains(console.String(), "manager-reload-marker") {
		t.Fatal("Manager did not follow logger reconfiguration")
	}
	after, _ := os.ReadFile(path)
	if bytes.Contains(after, []byte("manager-reload-marker")) {
		t.Fatal("Manager retained stale file output")
	}
}

func TestLoggingFileFailureIsStartupError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	// Deterministic on Windows too: a directory cannot be opened as main.log.
	if err := os.MkdirAll(filepath.Join(dir, "logs", "main.log"), 0700); err != nil {
		t.Fatal(err)
	}
	_, closeLogging, err := coreembedded.ConfigureLogging(&config.Config{LoggingToFile: true})
	if closeLogging != nil {
		closeLogging()
	}
	if err == nil || !strings.Contains(err.Error(), "main.log") {
		t.Fatalf("log file failure was hidden: %v", err)
	}
}

func TestInitializeEnablesFileLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := initialize(path, filepath.Join(dir, "data"), "127.0.0.1:8317"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"logging-to-file: true", "logs-max-total-size-mb: 100"} {
		if !strings.Contains(string(data), field) {
			t.Errorf("init config missing %s", field)
		}
	}
}

func TestExplicitConsoleLoggingIsRespected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	path, closeLogging, err := coreembedded.ConfigureLogging(&config.Config{LoggingToFile: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeLogging)
	if path != "" {
		t.Fatalf("console logging reported a file: %s", path)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "main.log")); !os.IsNotExist(err) {
		t.Fatalf("explicit console mode created a file: %v", err)
	}
	var console bytes.Buffer
	log.SetOutput(&console)
	log.Info("core-console-marker")
	stdlog.Print("manager-console-marker")
	for _, marker := range []string{"core-console-marker", "manager-console-marker"} {
		if !strings.Contains(console.String(), marker) {
			t.Errorf("missing %s", marker)
		}
	}
}

func TestProductLogDefaultsDoNotLeakIntoStandaloneSDK(t *testing.T) {
	parse := func() *config.Config {
		cfg, err := config.ParseConfigBytes([]byte("host: 127.0.0.1\nport: 8317\n"))
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	before := parse()
	if before.LoggingToFile || before.LogsMaxTotalSizeMB != 0 {
		t.Fatal("standalone defaults were changed")
	}
	outer := coreembedded.UseProductLoggingDefaults()
	inner := coreembedded.UseProductLoggingDefaults()
	outer()
	outer() // release is idempotent; the inner scope still owns defaults.
	active := parse()
	if !active.LoggingToFile || active.LogsMaxTotalSizeMB != 100 {
		inner()
		t.Fatal("nested embedding scope lost product defaults")
	}
	inner()
	inner()
	after := parse()
	if after.LoggingToFile || after.LogsMaxTotalSizeMB != 0 {
		t.Fatal("product defaults leaked past their owner")
	}
}

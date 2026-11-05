package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Run in a new process: time caches its zone source and GOROOT in globals.
// On Windows this reproduces an installed executable without a Go SDK/IANA DB.
func TestPortableTimeZonesWithoutGOROOT(t *testing.T) {
	if os.Getenv("CLIPROXY_PLUS_TZ_HELPER") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPortableTimeZonesWithoutGOROOT$", "-test.v")
		for _, item := range os.Environ() {
			name, _, _ := strings.Cut(item, "=")
			switch strings.ToUpper(name) {
			case "GOROOT", "ZONEINFO", "CLIPROXY_PLUS_TZ_HELPER":
			default:
				command.Env = append(command.Env, item)
			}
		}
		empty := t.TempDir()
		command.Env = append(command.Env, "GOROOT="+empty, "ZONEINFO="+empty, "CLIPROXY_PLUS_TZ_HELPER=1")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("portable timezone lookup failed: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}
	for _, sample := range []struct {
		name   string
		month  time.Month
		offset int
	}{
		{"Asia/Shanghai", time.November, 8 * 3600},
		{"Asia/Kolkata", time.January, 5*3600 + 1800},
		{"America/New_York", time.January, -5 * 3600},
		{"America/New_York", time.July, -4 * 3600},
		{"Europe/London", time.January, 0},
		{"Europe/London", time.July, 3600},
	} {
		location, err := time.LoadLocation(sample.name)
		if err != nil {
			t.Errorf("LoadLocation(%q) without GOROOT/ZONEINFO: %v", sample.name, err)
			continue
		}
		_, offset := time.Date(2026, sample.month, 15, 12, 0, 0, 0, time.UTC).In(location).Zone()
		if offset != sample.offset {
			t.Errorf("%s %s offset=%d want=%d", sample.name, sample.month, offset, sample.offset)
		}
	}
	if _, err := time.LoadLocation("Mars/Olympus"); err == nil {
		t.Error("invalid timezone was silently accepted")
	}
}

package sourceupdate

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"cliproxyapi-plus/internal/webupdate"
)

// Rebuilds the known baseline through the actual owned worker without installing
// anything. An explicit test-only pinned target map can exercise a real newer
// upstream. Public APIs still reject no-op upgrades; this is a developer gate.
func TestBaselineSourceBuildPipeline(t *testing.T) {
	if os.Getenv("CLIPROXY_PLUS_SOURCE_BUILD_TEST") != "1" {
		t.Skip("opt-in full baseline source pipeline acceptance")
	}
	binary := os.Getenv("CLIPROXY_PLUS_SOURCE_BINARY_TEST")
	workspace := os.Getenv("CLIPROXY_PLUS_SOURCE_KIT_TEST")
	tools := os.Getenv("CLIPROXY_PLUS_SOURCE_TOOLS_TEST")
	reportPath := os.Getenv("CLIPROXY_PLUS_SOURCE_BUILD_REPORT")
	if binary == "" || workspace == "" || tools == "" || reportPath == "" {
		t.Fatal("all acceptance paths must be explicit")
	}
	raw, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		Version        string `json:"version"`
		Source         string `json:"product_source_sha256"`
		Lock           string `json:"upstream_lock_sha256"`
		CoreVersion    string `json:"core_version"`
		CoreCommit     string `json:"core_commit"`
		ManagerVersion string `json:"manager_version"`
		ManagerCommit  string `json:"manager_commit"`
	}
	if json.Unmarshal(raw, &identity) != nil {
		t.Fatal("invalid binary identity")
	}
	current := map[string]webupdate.SourceTarget{"cliproxyapi": {Version: identity.CoreVersion, Commit: identity.CoreCommit}, "cpa-manager-plus": {Version: identity.ManagerVersion, Commit: identity.ManagerCommit}}
	opts := Options{Enabled: true, Workspace: workspace, ToolsFile: tools, SourceDigest: identity.Source, LockDigest: identity.Lock, Version: identity.Version, Current: current}
	targets := map[string]webupdate.SourceTarget{"cliproxyapi": current["cliproxyapi"]}
	if raw := os.Getenv("CLIPROXY_PLUS_SOURCE_TARGETS_TEST"); raw != "" {
		if json.Unmarshal([]byte(raw), &targets) != nil || len(targets) == 0 {
			t.Fatal("invalid pinned target test input")
		}
		for name, target := range targets {
			if (name != "cliproxyapi" && name != "cpa-manager-plus") || !commitPattern.MatchString(target.Commit) {
				t.Fatal("invalid target identity")
			}
		}
	}
	expected := map[string]webupdate.SourceTarget{}
	for name, target := range current {
		expected[name] = target
	}
	for name, target := range targets {
		expected[name] = target
	}
	baseline := expected["cliproxyapi"].Commit == identity.CoreCommit && expected["cpa-manager-plus"].Commit == identity.ManagerCommit
	controller := New(opts, nil)
	job := sourceAcceptanceJob(t)
	defer func() {
		if log, err := os.ReadFile(filepath.Join(job, "source-build.log")); err == nil {
			_ = os.WriteFile(filepath.Join(filepath.Dir(reportPath), "logs", "source-updates-full-source-build.log"), log, 0600)
		}
	}()
	phases := []string{}
	result, err := controller.build(context.Background(), webupdate.SourceRequest{Current: current, Targets: targets}, job, func(phase string) { phases = append(phases, phase); t.Log("source phase:", phase) })
	if err != nil {
		if rawLog, readErr := os.ReadFile(filepath.Join(job, "source-build.log")); readErr == nil {
			if _, sourceFile, _, ok := runtime.Caller(0); ok {
				goNames, pythonNames := knownDiagnosticTestNames(filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..")))
				summary := buildFailureDiagnostics(rawLog, goNames, pythonNames, knownDiagnosticScriptFrames(filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))))
				encoded, _ := json.Marshal(summary)
				t.Logf("source-build failure diagnostics (source identifiers only): %s", encoded)
			}
		}
		t.Fatal(err)
	}
	if result.Version != identity.Version || result.CoreCommit != expected["cliproxyapi"].Commit || result.ManagerCommit != expected["cpa-manager-plus"].Commit || result.SourceDigest != identity.Source {
		t.Fatal("baseline source build changed the approved pair/recipe")
	}
	kit, err := loadKit(result.Workspace)
	if err != nil || kit.SourceDigest != result.SourceDigest {
		t.Fatal("next source recipe is not ready")
	}
	if _, err := os.Stat(result.Archive); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"passed": true, "product_source_sha256": identity.Source, "baseline_rebuild_only": baseline, "install_requested": false, "extra_release_service": false, "time": time.Now().UTC().Format(time.RFC3339), "phases": phases, "core_commit": result.CoreCommit, "manager_commit": result.ManagerCommit, "archive_sha256": result.SHA256, "tools_config": tools, "checks": []string{"bundled SDKs reused without SDK downloads", "actual Go-owned build process tree", "verified private recipe copy", "fixed-SHA GitHub source download", "unselected partner kept pinned", "candidate Node engine enforcement", "complete upstreams.validate gates including test-source", "prepared next recipe and integrated archive", "no running application or data replaced"}}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(reportPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

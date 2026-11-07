package webupdate

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Stable identifiers only: public diagnostics never contain compiler output,
// private paths, proxy credentials, environment values or log excerpts.
var sourceSteps = []string{"checking_tools", "verifying_recipe", "fetching_source", "preparing_source", "frontend_dependencies", "frontend_build", "product_tests", "binary_build", "packaging", "single_port_test", "update_test", "recipe_test", "frontend_tests", "adapter_tests", "binary_smoke", "maintenance_smoke", "verifying_artifact"}

type BuildProgress struct {
	Step           string `json:"step"`
	Completed      int    `json:"completed"`
	Total          int    `json:"total"`
	StepStartedAt  string `json:"step_started_at"`
	LastActivityAt string `json:"last_activity_at"`
}

func SourceStepIndex(step string) int {
	for index, name := range sourceSteps {
		if step == name {
			return index
		}
	}
	return -1
}
func recordSourceProgress(op *Operation, step string) {
	index := SourceStepIndex(step)
	if index < 0 {
		return
	}
	if op.Progress != nil && index < op.Progress.Completed {
		return
	}
	now := stamp()
	started := now
	if op.Progress != nil && op.Progress.Step == step {
		started = op.Progress.StepStartedAt
	}
	op.Progress = &BuildProgress{Step: step, Completed: index, Total: len(sourceSteps), StepStartedAt: started, LastActivityAt: now}
	switch step {
	case "checking_tools", "verifying_recipe", "fetching_source":
		op.State = "fetching_source"
	default:
		op.State = "validating_source"
	}
}

var sourceFailureMessages = map[string]string{
	"go_dependency_network":  "Go dependency download failed. Configure a reachable GOPROXY or HTTPS_PROXY for the build and retry; checksum verification remains enabled.",
	"npm_dependency_network": "Frontend dependency download failed. Check the npm registry and build network proxy, then retry.",
	"source_network":         "Upstream source download failed. Check GitHub connectivity or the build network proxy, then retry.",
	"patch_incompatible":     "The target upstream source does not match the verified integration patches. A compatible integration recipe is required.",
	"toolchain_incompatible": "The target upstream requires a different build tool version. Use a verified compatible SDK package.",
	"tools_unavailable":      "Bundled build tools failed integrity, platform or version checks. Reinstall the complete matching package.",
	"recipe_invalid":         "The source recipe does not match this program or failed integrity checks. Reinstall its matching updater-source directory.",
	"source_cancelled":       "The source build was cancelled before installation.",
	"source_timeout":         "The source build exceeded its time limit before installation. Check build resources and connectivity.",
	"source_build_failed":     "The source build or compatibility checks failed before installation. Review the private source-build.log for this operation.",
	"shutdown_timeout":        "The running service did not stop within the drain deadline. Retained running version to preserve continuity.",
	"shutdown_not_clean":      "Original service did not complete smooth shutdown; cancelled update to avoid inconsistency and kept running version.",
	"backup_failed":           "Data snapshot backup failed before replacement; cancelled update safely and resumed original program.",
	"database_locked":         "Database is locked by another process; snapshot backup cannot proceed safely, update cancelled.",
	"replace_binary_failed":   "Binary executable replacement failed; automatically rolled back and kept original service running.",
	"check_data_failed":       "Offline data migration and integrity check failed; automatically restored original program and data snapshot.",
	"replacement_exited":      "Replacement program exited unexpectedly after launch; automatically triggered rollback to original version.",
	"health_deadline_exceeded": "Replacement program failed to pass health checks before deadline; automatically rolled back to original version.",
	"rollback_failed":         "Rollback restoration failed; inspect local backup or perform offline recovery using the operation ID.",
	"worker_handshake_failed": "Update helper process handshake timed out; original service continues running normally.",
	"maintenance_failed":      "Maintenance did not complete normally. Review the private transaction log for this operation.",
}

func safeSourceCode(code string) string {
	if _, ok := sourceFailureMessages[code]; ok {
		return code
	}
	return ""
}

type sourceBuildError struct {
	code  string
	cause error
}

func (e *sourceBuildError) Error() string { return e.cause.Error() }
func (e *sourceBuildError) Unwrap() error { return e.cause }
func SourceBuildFailure(code string, cause error) error {
	if cause == nil {
		cause = errors.New("source build failed")
	}
	if safeSourceCode(code) == "" {
		code = "source_build_failed"
	}
	return &sourceBuildError{code: code, cause: cause}
}
func sourceErrorCode(err error) string {
	var failure *sourceBuildError
	if errors.As(err, &failure) {
		return safeSourceCode(failure.code)
	}
	return "source_build_failed"
}

func ClassifySourceFailure(line string) string {
	lower := strings.ToLower(line)
	network := false
	for _, word := range []string{"dial tcp", "connectex:", "i/o timeout", "tls handshake timeout", "no such host", "enotfound", "etimedout", "econnreset", "eai_again", "unable to get local issuer certificate", "certificate verify failed", "connection refused", "network is unreachable"} {
		network = network || strings.Contains(lower, word)
	}
	if network {
		if strings.Contains(lower, "npm error") || strings.Contains(lower, "npm err!") {
			return "npm_dependency_network"
		}
		if strings.Contains(lower, "get \"") && (strings.Contains(lower, "/@v/") || strings.Contains(lower, "proxy.golang.org") || strings.Contains(lower, "sum.golang.org")) {
			return "go_dependency_network"
		}
		if strings.Contains(lower, "urlerror") || strings.Contains(lower, "urllib.error") {
			return "source_network"
		}
	}
	if strings.Contains(lower, "patch preimage drift") || strings.Contains(lower, "patch context count mismatch") {
		return "patch_incompatible"
	}
	if strings.Contains(lower, "required node engine:") || strings.Contains(lower, "requires go >=") || strings.Contains(lower, "unsupported engine") {
		return "toolchain_incompatible"
	}
	return ""
}

func publicBuildProgress(p *BuildProgress) *BuildProgress {
	if p == nil || SourceStepIndex(p.Step) < 0 || p.Total != len(sourceSteps) || p.Completed < 0 || p.Completed > p.Total {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, p.StepStartedAt); err != nil {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, p.LastActivityAt); err != nil {
		return nil
	}
	if p.Completed != SourceStepIndex(p.Step) && !(p.Completed == p.Total && SourceStepIndex(p.Step) == p.Total-1) {
		return nil
	}
	result := *p
	return &result
}

func publicSourceCode(op Operation) string {
	if op.Error == "" {
		return ""
	}
	if code := safeSourceCode(op.FailureCode); code != "" {
		return code
	}
	// Backward-compatible diagnosis for pre-install failures from older versions.
	// Do not mislabel an installation/migration failure using old build output.
	if !idPattern.MatchString(op.ID) || op.State != "failed" || op.SourceRequest == nil || op.TargetHash != "" || op.SourceWorkspace != "" {
		return ""
	}
	path := filepath.Join(op.Options.operationDir(op.ID), "source-build.log")
	if noLinks(path) != nil {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	start := info.Size() - 65536
	if start < 0 {
		start = 0
	}
	if _, err = file.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65536))
	if err != nil {
		return ""
	}
	code := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if found := ClassifySourceFailure(line); found != "" {
			code = found
		}
	}
	return code
}

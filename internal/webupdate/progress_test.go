package webupdate

import (
	"cliproxyapi-plus/internal/product"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacySourceNetworkFailureIsActionableAndPrivate(t *testing.T) {
	o := fixture(t)
	op := transaction(t, o)
	op.State = "failed"
	op.Error = "source staging/build/compatibility validation failed"
	op.SourceRequest = &SourceRequest{}
	line := `Get "https://private-user:private-password@proxy.golang.org/private-module/@v/v1.mod": dial tcp 142.250.204.49:443: connectex: connection attempt failed`
	if err := os.WriteFile(filepath.Join(o.operationDir(op.ID), "source-build.log"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	result := publicOperation(op)
	if result["failure_code"] != "go_dependency_network" {
		t.Fatalf("network failure is hidden: %v", result)
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"private-password", "private-user", "private-module", o.Data, line} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private diagnostic leaked", secret)
		}
	}
}

func TestSourceProgressIsMonotoneAndDoesNotAnnounceInstallation(t *testing.T) {
	op := Operation{State: "fetching_source"}
	recordSourceProgress(&op, "checking_tools")
	if op.Progress.Completed != 0 || op.Progress.Total != 17 {
		t.Fatal(op.Progress)
	}
	recordSourceProgress(&op, "product_tests")
	started := op.Progress.StepStartedAt
	recordSourceProgress(&op, "product_tests")
	if op.Progress.StepStartedAt != started {
		t.Fatal("step timer was reset")
	}
	recordSourceProgress(&op, "fetching_source")
	recordSourceProgress(&op, "succeeded")
	if op.State != "validating_source" || op.Progress.Step != "product_tests" || op.Progress.Completed != 6 {
		t.Fatal(op)
	}
	if publicBuildProgress(op.Progress) == nil {
		t.Fatal("valid progress was hidden")
	}
	op.Progress.Step = "private-secret"
	if publicBuildProgress(op.Progress) != nil {
		t.Fatal("unrecognized progress reached public API")
	}
}

func TestSourceFailureClassifierIsFixedCatalogOnly(t *testing.T) {
	cases := map[string]string{
		`Get "https://proxy.golang.org/example/module/@v/v1.mod": dial tcp: i/o timeout`: "go_dependency_network",
		`npm error code ETIMEDOUT`:                                    "npm_dependency_network",
		`urllib.error.URLError: <urlopen error no such host>`:         "source_network",
		`Patch preimage drift: sensitive/path`:                        "patch_incompatible",
		`Required Node engine: >=100`:                                 "toolchain_incompatible",
		`https://secret:password@private.invalid/config secret-token`: "",
	}
	for line, want := range cases {
		if got := ClassifySourceFailure(line); got != want {
			t.Errorf("classification %q != %q", got, want)
		}
	}
}

func TestLegacyDiagnosisDoesNotMislabelInstallationFailure(t *testing.T) {
	o := fixture(t)
	op := transaction(t, o)
	op.State = "failed"
	op.Error = "private installation failure"
	op.SourceRequest = &SourceRequest{}
	_ = os.WriteFile(filepath.Join(o.operationDir(op.ID), "source-build.log"), []byte(`Get "https://proxy.golang.org/a/@v/v1.mod": dial tcp: i/o timeout`), 0600)
	op.SourceWorkspace = "validated recipe"
	if publicOperation(op)["failure_code"] != "" {
		t.Fatal("installation error mislabeled as an old network failure")
	}
	op.FailureCode = "secret:password"
	raw, _ := json.Marshal(publicOperation(op))
	if strings.Contains(string(raw), "secret:password") {
		t.Fatal("unknown error code was exposed")
	}
}

func TestActualFailedOperationReadOnlyDiagnosis(t *testing.T) {
	path := os.Getenv("CLIPROXY_PLUS_FAILED_OPERATION_TEST")
	if path == "" {
		t.Skip("operator-authorized read-only diagnostic")
	}
	var op Operation
	if err := readJSON(path, &op); err != nil {
		t.Fatal(err)
	}
	if op.State != "failed" || op.SourceRequest == nil {
		t.Fatal("only inspect a failed source build")
	}
	result := publicOperation(op)
	if result["failure_code"] != "go_dependency_network" {
		t.Fatal("actual failure was not identified", result["failure_code"])
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), op.Options.Data) || strings.Contains(string(raw), op.Options.Binary) {
		t.Fatal("private installation paths leaked")
	}
	t.Log("actual pre-install failure classified as go_dependency_network; journal was not modified")
}

func TestPersistedSourceProgressSurvivesSnapshotsWithoutStoppingService(t *testing.T) {
	o := fixture(t)
	releases, _ := product.NewReleases("", o.Version)
	c := New(context.Background(), o, releases, func() { t.Error("build failure must not stop service") })
	defer c.Close(false)
	current := map[string]SourceTarget{"cliproxyapi": {Version: "v8.0.53", Commit: strings.Repeat("a", 40)}, "cpa-manager-plus": {Version: "v1.14.10", Commit: strings.Repeat("b", 40)}}
	gate := make(chan struct{})
	built := make(chan struct{})
	request := SourceRequest{Current: current, Targets: map[string]SourceTarget{"cliproxyapi": {Version: "v8.0.54", Commit: strings.Repeat("c", 40)}}}
	response, err := c.StartSource(request, func(_ context.Context, _ SourceRequest, _ string, phase func(string)) (SourceCandidate, error) {
		defer close(built)
		<-gate
		for _, step := range []string{"checking_tools", "verifying_recipe", "fetching_source", "preparing_source", "product_tests", "activity", "checking_tools"} {
			phase(step)
		}
		return SourceCandidate{}, SourceBuildFailure("go_dependency_network", errors.New("private error password"))
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := response["progress"].(*BuildProgress)
	close(gate)
	<-built
	deadline := time.Now().Add(5 * time.Second)
	var record Operation
	for time.Now().Before(deadline) {
		record, err = readOperation(o, response["id"].(string))
		if err == nil && record.State == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || record.State != "failed" || record.Progress.Step != "product_tests" || record.FailureCode != "go_dependency_network" {
		t.Fatal(record.State, record.Progress, err)
	}
	if initial.Step != "checking_tools" || initial.Completed != 0 {
		t.Fatal("accepted response aliased mutable worker progress")
	}
	result := c.Snapshot()
	history := result["history"].([]map[string]any)
	if len(history) != 1 || history[0]["failure_code"] != "go_dependency_network" || result["busy"] != false {
		t.Fatal(result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private error password") {
		t.Fatal("raw error leaked")
	}
	data, _ := os.ReadFile(o.Binary)
	if string(data) != "original binary" {
		t.Fatal("failure changed installed program")
	}
}

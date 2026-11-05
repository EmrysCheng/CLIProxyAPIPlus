package webupdate

import (
	"cliproxyapi-plus/internal/product"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSourceRecipeRestartAndRollbackUseBinaryIdentity(t *testing.T) {
	original := []string{"--config", "config", "--source-updates", "--source-workspace", "old-kit", "--source-tools", "tools.json"}
	next := replaceSourceWorkspace(original, "new-kit")
	if !reflect.DeepEqual(original, []string{"--config", "config", "--source-updates", "--source-workspace", "old-kit", "--source-tools", "tools.json"}) {
		t.Fatal("mutated original restart recipe")
	}
	op := Operation{FromVersion: "1.0.0", TargetVersion: "1.0.0", OriginalHash: "old", TargetHash: "new", Options: Options{Args: original}, TargetArgs: next}
	if !reflect.DeepEqual(restartArgs(op, "new"), next) || !reflect.DeepEqual(restartArgs(op, "old"), original) {
		t.Fatal("same product version selected wrong source recipe")
	}
	// A manual rollback targets the former operation's exact original arguments.
	rollback := Operation{OriginalHash: "new", TargetHash: "old", Options: Options{Args: next}, TargetArgs: original}
	if !reflect.DeepEqual(restartArgs(rollback, "old"), original) || !reflect.DeepEqual(restartArgs(rollback, "new"), next) {
		t.Fatal("rollback/failure recipe mismatch")
	}
}
func TestSourceSelectionRejectsNoopAndUnknownProjects(t *testing.T) {
	o := fixture(t)
	releases, _ := product.NewReleases("", o.Version)
	c := New(context.Background(), o, releases, func() { t.Error("must not stop") })
	defer c.Close(false)
	current := map[string]SourceTarget{"cliproxyapi": {Version: "v8.0.53", Commit: strings.Repeat("a", 40)}, "cpa-manager-plus": {Version: "v1.14.10", Commit: strings.Repeat("b", 40)}}
	builder := func(context.Context, SourceRequest, string, func(string)) (SourceCandidate, error) {
		return SourceCandidate{}, errors.New("must not build")
	}
	if _, err := c.StartSource(SourceRequest{Current: current, Targets: map[string]SourceTarget{"cliproxyapi": current["cliproxyapi"]}}, builder); err == nil {
		t.Fatal("no-op source upgrade accepted")
	}
	if _, err := c.StartSource(SourceRequest{Current: current, Targets: map[string]SourceTarget{"attacker": {Version: "1.0.0", Commit: strings.Repeat("c", 40)}}}, builder); err == nil {
		t.Fatal("unknown repository accepted")
	}
}

func TestSourceArtifactUsesNoHTTPAndChecksBothCommits(t *testing.T) {
	o := fixture(t)
	releases, _ := product.NewReleases("", o.Version)
	c := New(context.Background(), o, releases, func() { t.Fatal("must not stop while preparing") })
	defer c.Close(false)
	op := transaction(t, o)
	op.SourceRequest = &SourceRequest{}
	op.TargetVersion = o.Version
	op.SourceCoreCommit = strings.Repeat("c", 40)
	op.SourceManagerCommit = strings.Repeat("b", 40)
	op.TargetSourceDigest = strings.Repeat("a", 64)
	archive := filepath.Join(o.operationDir(op.ID), "built.zip")
	if err := os.WriteFile(archive, []byte("private prepared package"), 0600); err != nil {
		t.Fatal(err)
	}
	sum, err := fileHash(archive)
	if err != nil {
		t.Fatal(err)
	}
	result := SourceCandidate{Archive: archive, SHA256: sum}
	if err := writeJSON(filepath.Join(o.operationDir(op.ID), "source-result.json"), result); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(o.operationDir(op.ID), "package")
	if err := c.materializeArchive(op, product.Asset{SHA256: sum}, target); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "private prepared package" {
		t.Fatal("source package did not reach existing installer")
	}
	identity := binaryIdentity{Version: o.Version, CoreCommit: op.SourceCoreCommit, ManagerCommit: op.SourceManagerCommit, SourceDigest: op.TargetSourceDigest, SourceUpdateProtocol: 1}
	if !candidateMatches(op, identity, false) {
		t.Fatal("valid same-version source update rejected")
	}
	wrong := identity
	wrong.ManagerCommit = strings.Repeat("d", 40)
	if candidateMatches(op, wrong, false) {
		t.Fatal("unselected partner commit changed")
	}
	wrong = identity
	wrong.SourceUpdateProtocol = 0
	if candidateMatches(op, wrong, false) {
		t.Fatal("source-incapable program accepted")
	}
	wrong = identity
	wrong.SourceDigest = strings.Repeat("0", 64)
	if candidateMatches(op, wrong, false) {
		t.Fatal("source identity mismatch accepted")
	}
	if err := os.WriteFile(archive, []byte("tampered package"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.materializeArchive(op, product.Asset{SHA256: sum}, filepath.Join(o.operationDir(op.ID), "tampered")); err == nil {
		t.Fatal("modified source artifact accepted")
	}
}

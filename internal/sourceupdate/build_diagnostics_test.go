package sourceupdate

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestBuildFailureDiagnosticsOnlyExposeKnownSourceIdentifiers(t *testing.T) {
	raw := []byte("--- FAIL: TestKnown/subcase-with-private-value (0.1s)\n--- FAIL: TestNeverPublishSecretValue (0.2s)\nFAIL: test_known (private.module)\nSECRET=do-not-print https://private.example/token\nPermissionError: [WinError 5] denied at C:\\private\\data\n")
	summary := buildFailureDiagnostics(raw, map[string]bool{"TestKnown": true}, map[string]bool{"test_known": true})
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"NeverPublish", "private", "SecretValue", "do-not-print", "PermissionError", "subcase"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe diagnostic %q", text)
		}
	}
	for _, expected := range []string{"TestKnown", "test_known", "windows_access_denied"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing safe diagnostic %q: %s", expected, text)
		}
	}
}

func TestBuildFailureDiagnosticsStayBounded(t *testing.T) {
	raw := []byte(strings.Repeat("--- FAIL: TestKnown (0.1s)\n", 1000))
	summary := buildFailureDiagnostics(raw, map[string]bool{"TestKnown": true}, nil)
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 2048 || strings.Count(string(encoded), "TestKnown") != 1 {
		t.Fatalf("unbounded diagnostics: %s", encoded)
	}
}

// Public CI diagnostics contain only identifiers already declared in the reviewed
// product test sources, never raw output, subtest values, paths or environment.
type buildDiagnosticSummary struct {
	GoTests         []string `json:"go_tests,omitempty"`
	PythonTests     []string `json:"python_tests,omitempty"`
	ObservedSignals []string `json:"observed_signals,omitempty"`
}

var goFailureName = regexp.MustCompile(`(?m)^\s*--- FAIL: (Test[A-Za-z0-9_]+)(?:[/\s(])`)
var pythonFailureName = regexp.MustCompile(`(?m)^\s*(?:FAIL|ERROR): (test_[A-Za-z0-9_]+)(?:[\s(])`)
var pythonDeclaredName = regexp.MustCompile(`(?m)^[\t ]*def (test_[A-Za-z0-9_]+)\(`)
var publicGoTestName = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)

func knownDiagnosticTestNames(root string) (map[string]bool, map[string]bool) {
	goNames, pythonNames := map[string]bool{}, map[string]bool{}
	for _, directory := range []string{"cmd", "internal"} {
		_ = filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry == nil || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			source, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return nil
			}
			for _, declaration := range source.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if ok && function.Recv == nil && publicGoTestName.MatchString(function.Name.Name) {
					goNames[function.Name.Name] = true
				}
			}
			return nil
		})
	}
	entries, _ := os.ReadDir(filepath.Join(root, "tests"))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(entry.Name(), ".py") {
			continue
		}
		source, err := os.ReadFile(filepath.Join(root, "tests", entry.Name()))
		if err != nil {
			continue
		}
		for _, match := range pythonDeclaredName.FindAllSubmatch(source, -1) {
			pythonNames[string(match[1])] = true
		}
	}
	return goNames, pythonNames
}

func buildFailureDiagnostics(raw []byte, knownGo, knownPython map[string]bool) buildDiagnosticSummary {
	names := func(pattern *regexp.Regexp, known map[string]bool) []string {
		seen := map[string]bool{}
		for _, match := range pattern.FindAllSubmatch(raw, -1) {
			name := string(match[1])
			if known[name] && len(name) <= 160 {
				seen[name] = true
			}
		}
		result := make([]string, 0, len(seen))
		for name := range seen {
			result = append(result, name)
		}
		sort.Strings(result)
		if len(result) > 16 {
			result = result[:16]
		}
		return result
	}
	summary := buildDiagnosticSummary{GoTests: names(goFailureName, knownGo), PythonTests: names(pythonFailureName, knownPython)}
	text := string(raw)
	for _, signal := range []struct{ marker, code string }{
		{"panic: test timed out after", "go_test_timeout"},
		{"WinError 5", "windows_access_denied"},
		{"WinError 32", "windows_sharing_violation"},
		{"[build failed]", "go_compile_failure"},
		{"[setup failed]", "go_setup_failure"},
		{"Access is denied.", "windows_access_denied"},
		{"The system cannot find the file specified", "windows_file_not_found"},
		{"The directory name is invalid", "windows_invalid_directory"},
		{"missing go.sum entry", "go_module_checksum_missing"},
		{"checksum mismatch", "dependency_checksum_mismatch"},
		{"403 Forbidden", "dependency_http_forbidden"},
		{"429 Too Many Requests", "dependency_http_rate_limited"},
		{"502 Bad Gateway", "dependency_http_gateway_failure"},
		{"504 Gateway Time-out", "dependency_http_gateway_timeout"},
		{"Unrecognized import path", "go_import_resolution_failure"},
		{"no such host", "network_dns_failure"},
		{"TLS handshake timeout", "network_tls_timeout"},
		{"certificate signed by unknown authority", "network_certificate_failure"},
		{"connection was forcibly closed", "network_connection_reset"},
		{"i/o timeout", "network_io_timeout"},
		{"unexpected EOF", "unexpected_eof"},
		{"The filename or extension is too long", "windows_path_too_long"},
		{"The system cannot find the path specified", "windows_path_not_found"},
		{"not enough memory", "memory_exhausted"},
		{"out of memory", "memory_exhausted"},
		{"no space left on device", "disk_exhausted"},
		{"Traceback (most recent call last):", "python_traceback"},
		{"ModuleNotFoundError:", "python_module_missing"},
		{"ImportError:", "python_import_failure"},
		{"FileNotFoundError:", "python_file_missing"},
		{"Owned build process failed: exit status 1", "worker_exit_one"},
	} {
		if strings.Contains(text, signal.marker) {
			summary.ObservedSignals = append(summary.ObservedSignals, signal.code)
		}
	}
	return summary
}

func TestKnownDiagnosticNamesAreReadFromDeclaredTestSources(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"cmd/sample", "tests"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "cmd/sample/main_test.go"), []byte("package main\nfunc TestPublic(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd/sample/ordinary.go"), []byte("package main\nfunc TestOther(t *testing.T) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tests/test_sample.py"), []byte("class Tests:\n    def test_public(self): pass\n"), 0600); err != nil {
		t.Fatal(err)
	}
	goNames, pythonNames := knownDiagnosticTestNames(root)
	if !goNames["TestPublic"] || goNames["TestOther"] || !pythonNames["test_public"] {
		t.Fatalf("unexpected public name inventory: %v %v", goNames, pythonNames)
	}
}

func TestBuildFailureDiagnosticsClassifySetupErrorsWithoutPublishingOutput(t *testing.T) {
	raw := []byte("FAIL cliproxyapi-plus/internal/x [setup failed]\nGet https://private.example/secret: dial tcp: no such host\nTraceback (most recent call last):\n")
	summary := buildFailureDiagnostics(raw, nil, nil)
	encoded, _ := json.Marshal(summary)
	text := string(encoded)
	for _, expected := range []string{"go_setup_failure", "network_dns_failure", "python_traceback"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing fixed signal %s: %s", expected, text)
		}
	}
	if strings.Contains(text, "private") || strings.Contains(text, "secret") {
		t.Fatal("raw output was exposed")
	}
}

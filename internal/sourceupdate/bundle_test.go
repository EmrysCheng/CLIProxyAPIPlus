package sourceupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func toolBundleFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	records := map[string]string{}
	tools := Tools{Python: "python/run", Go: "go/run", Node: "node/run", NpmCLI: "npm/run.js"}
	for _, name := range []string{tools.Python, tools.Go, tools.Node, tools.NpmCLI} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("fixture"))
		records[name] = hex.EncodeToString(sum[:])
	}
	raw, _ := json.Marshal(toolchainManifest{Schema: 1, Target: runtime.GOOS + "/" + runtime.GOARCH, Tools: tools, Records: records, Executables: []string{tools.Python}})
	if err := os.WriteFile(filepath.Join(root, "toolchain-manifest.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return root, hex.EncodeToString(sum[:])
}

func TestToolBundleTrustAndInventory(t *testing.T) {
	for _, change := range []string{"none", "manifest", "content", "missing", "extra", "wrong-target", "wrong-pin", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			root, pin := toolBundleFixture(t)
			target := runtime.GOOS + "/" + runtime.GOARCH
			ctx := context.Background()
			switch change {
			case "manifest":
				_ = os.WriteFile(filepath.Join(root, "toolchain-manifest.json"), []byte(`{}`), 0600)
			case "content":
				_ = os.WriteFile(filepath.Join(root, "python/run"), []byte("changed"), 0700)
			case "missing":
				_ = os.Remove(filepath.Join(root, "node/run"))
			case "extra":
				_ = os.WriteFile(filepath.Join(root, "extra.py"), []byte("extra"), 0600)
			case "wrong-target":
				target = "unapproved/platform"
			case "wrong-pin":
				pin = strings.Repeat("0", 64)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			tools, err := verifyToolBundle(ctx, root, target, pin)
			if change == "none" {
				if err != nil || tools.Bundle != root || !filepath.IsAbs(tools.Go) {
					t.Fatal(tools, err)
				}
			} else if err == nil {
				t.Fatal("untrusted bundle accepted", change)
			}
		})
	}
}

func TestCorruptBundleNeverFallsBackToPath(t *testing.T) {
	root, _ := toolBundleFixture(t)
	config := filepath.Join(t.TempDir(), "update-tools.json")
	raw, _ := json.Marshal(Tools{Bundle: root})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, status := resolveTools(context.Background(), config)
	if status.Ready || status.Origin != "bundled" || len(status.Versions) != 0 {
		t.Fatal(status)
	}
	raw, _ = json.Marshal(Tools{Bundle: root, Python: "python"})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, status = resolveTools(context.Background(), config); status.Ready {
		t.Fatal("mixed bundle override accepted")
	}
}

func TestPackagedSDKWithoutHostTools(t *testing.T) {
	config := os.Getenv("CLIPROXY_PLUS_BUNDLED_TOOLS_TEST")
	if config == "" {
		t.Skip("opt-in real bundled SDK test")
	}
	// Leave only OS utilities in PATH; nonexistent host tool roots and injected
	// interpreter options must not interfere with the product-pinned SDKs.
	path := "/usr/bin:/bin"
	if runtime.GOOS == "windows" {
		path = filepath.Join(os.Getenv("SystemRoot"), "System32")
	}
	t.Setenv("PATH", path)
	t.Setenv("GOROOT", filepath.Join(t.TempDir(), "old Go"))
	t.Setenv("NODE_OPTIONS", "--require=nonexistent-host-injection")
	t.Setenv("PYTHONHOME", filepath.Join(t.TempDir(), "old Python"))
	started := time.Now()
	tools, status := resolveTools(context.Background(), config)
	if !status.Ready || status.Origin != "bundled" || tools.Bundle == "" {
		t.Logf("bundled readiness elapsed=%s", time.Since(started))
		if status.Origin == "bundled" && status.Versions["Node"] != "" && tools.Node != "" && tools.NpmCLI != "" {
			// Diagnose the same pinned npm on a cold/slow host, but NEVER turn
			// a failed readiness check into a pass. Do not log command output,
			// local configuration, environment values, or credential-bearing errors.
			probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(probeCtx, tools.Node, tools.NpmCLI, "--version")
			command.Env = toolEnvironment(tools)
			hideProbe(command)
			started := time.Now()
			raw, err := command.CombinedOutput()
			exitCode := 0
			if err != nil {
				exitCode = -1
				if exit, ok := err.(*exec.ExitError); ok {
					exitCode = exit.ExitCode()
				}
			}
			t.Logf("bundled npm diagnostic elapsed=%s exit=%d deadline=%t output_bytes=%d", time.Since(started), exitCode, probeCtx.Err() != nil, len(raw))
		}
		t.Fatal(status)
	}
	probe := func(path string, args ...string) string {
		command := exec.Command(path, args...)
		command.Env = toolEnvironment(tools)
		hideProbe(command)
		raw, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("SDK probe failed: %v: %s", err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	goroot := probe(tools.Go, "env", "GOROOT")
	if !strings.EqualFold(filepath.Clean(goroot), filepath.Dir(filepath.Dir(tools.Go))) {
		t.Fatal("host GOROOT was used", goroot)
	}
	probe(tools.Python, "-I", "-B", "-c", "import ssl, sqlite3, urllib.request, unittest, tarfile, zipfile; print(ssl.OPENSSL_VERSION)")
	probe(tools.Node, "-e", "const semver=require(require.resolve('semver',{paths:[process.argv[1]]})); if(!semver.satisfies(process.version,'^24.22.1'))process.exit(1)", filepath.Dir(filepath.Dir(tools.NpmCLI)))
	if _, err := bundledTools(context.Background(), tools.Bundle); err != nil {
		t.Fatal("SDK probes changed the immutable bundle", err)
	}
	if report := os.Getenv("CLIPROXY_PLUS_BUNDLED_TOOLS_REPORT"); report != "" {
		data := map[string]any{"passed": true, "time": time.Now().UTC().Format(time.RFC3339), "target": runtime.GOOS + "/" + runtime.GOARCH, "tools": status, "config": config, "checks": []string{"binary-pinned complete inventory", "no host SDK PATH", "host GOROOT/Python/Node injection ignored", "Python SSL/sqlite/urllib/stdlib", "bundled npm semver", "no SDK mutations after probes"}}
		raw, _ := json.MarshalIndent(data, "", "  ")
		if err := os.WriteFile(report, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("verified package SDKs with no host SDK PATH:", status.Versions)
}

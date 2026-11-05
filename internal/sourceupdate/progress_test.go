package sourceupdate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildNetworkSettingsAreOperatorOnlyAndTLSVerified(t *testing.T) {
	for _, proxy := range []string{"https://proxy.golang.org", "https://approved.example/go|https://proxy.golang.org", "https://approved.example,direct", "off"} {
		if err := validateBuildNetwork(Tools{GoProxy: proxy}); err != nil {
			t.Fatal(proxy, err)
		}
	}
	for _, proxy := range []string{"http://mirror.example", "https://user:secret@mirror.example", "https://mirror.example/?token=secret", "https://mirror.example|", "https://mirror.example\nGOSUMDB=off"} {
		if validateBuildNetwork(Tools{GoProxy: proxy}) == nil {
			t.Fatal("untrusted setting accepted", proxy)
		}
	}
	t.Setenv("GOPROXY", "https://old.example")
	t.Setenv("GOSUMDB", "sum.golang.org")
	t.Setenv("NPM_CONFIG_REGISTRY", "https://old-npm.example")
	env := toolEnvironment(Tools{GoProxy: "https://approved.example|https://proxy.golang.org", NpmRegistry: "https://approved-npm.example"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "old.example") || strings.Contains(joined, "old-npm.example") || !strings.Contains(joined, "GOSUMDB=sum.golang.org") || strings.Contains(joined, "GOSUMDB=off") {
		t.Fatal("network override changed checksum policy or duplicated settings")
	}
	config := filepath.Join(t.TempDir(), "bad-tools.json")
	_ = os.WriteFile(config, []byte(`{"bundle":"./build-tools","go_proxy":"https://user:secret@mirror.example"}`), 0600)
	_, status := resolveTools(context.Background(), config)
	if status.Ready || len(status.Versions) != 0 || strings.Contains(strings.Join(status.Missing, " "), "secret") {
		t.Fatal(status)
	}
}
func TestBuildOutputPublishesOnlyKnownProgressAndSafeDiagnosis(t *testing.T) {
	phases := []string{}
	output := &buildOutput{writer: io.Discard, left: 128, phase: func(value string) { phases = append(phases, value) }}
	_, _ = output.Write([]byte("PLUS_SOURCE_PHASE:frontend_build\nPLUS_SOURCE_PHASE:private-secret\nGet \"https://user:password@proxy.golang.org/example/@v/v1.mod\": dial tcp: i/o timeout\n"))
	if output.failureCode != "go_dependency_network" {
		t.Fatal(output.failureCode)
	}
	joined := strings.Join(phases, ",")
	if !strings.Contains(joined, "frontend_build") || strings.Contains(joined, "private-secret") || strings.Contains(joined, "password") {
		t.Fatal(phases)
	}
}

package policy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	coreembedded "github.com/router-for-me/CLIProxyAPI/v8/sdk/embedded"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrozenConfiguration(t *testing.T) {
	key := strings.Repeat("a", 64)
	base := &config.Config{}
	base.Host = "127.0.0.1"
	base.Port = 8317
	base.AuthDir = "auths"
	base.RemoteManagement.SecretKey = key
	base.RemoteManagement.DisableControlPanel = true
	base.RemoteManagement.DisableAutoUpdatePanel = true
	validate := Frozen(base, key, t.TempDir())
	if err := validate(base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*config.Config){func(c *config.Config) { c.Port++ }, func(c *config.Config) { c.Pprof.Enable = true }, func(c *config.Config) { c.Discovery.Enabled = true }, func(c *config.Config) { c.RemoteManagement.AllowRemote = true }, func(c *config.Config) { c.RemoteManagement.SecretKey = "changed" }, func(c *config.Config) { c.AuthDir = "elsewhere" }, func(c *config.Config) { c.RemoteManagement.DisableControlPanel = false }} {
		next := *base
		mutate(&next)
		if err := validate(&next); err == nil {
			t.Fatal("unsafe update accepted")
		}
	}
}

func TestParseLoadAndSaveEnforceInstalledPolicy(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	key := strings.Repeat("a", 64)
	raw := "host: 127.0.0.1\nport: 8317\nauth-dir: auths\nremote-management:\n  secret-key: " + key + "\n  disable-control-panel: true\n  disable-auto-update-panel: true\n"
	base, err := config.ParseConfigBytes([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := coreembedded.InstallConfigPolicy(Frozen(base, key, dir))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = coreembedded.InstallConfigPolicy(func(*config.Config) error { return nil }); err == nil {
		t.Fatal("duplicate policy accepted")
	}
	bad := strings.Replace(raw, "8317", "8318", 1)
	if _, err = config.ParseConfigBytes([]byte(bad)); err == nil {
		t.Fatal("parser bypassed embedded policy")
	}
	next := *base
	next.Port++
	if err = config.SaveConfigPreserveComments(file, &next); err == nil {
		t.Fatal("save bypassed embedded policy")
	}
	after, _ := os.ReadFile(file)
	if string(after) != raw {
		t.Fatal("rejected save modified config")
	}
	if err = os.WriteFile(file, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = config.LoadConfig(file); err == nil {
		t.Fatal("disk reload bypassed embedded policy")
	}
	release()
	again, err := coreembedded.InstallConfigPolicy(Frozen(base, key, dir))
	if err != nil {
		t.Fatal(err)
	}
	defer again()
	release()
	if _, err = config.ParseConfigBytes([]byte(bad)); err == nil {
		t.Fatal("stale cleanup removed a newer policy")
	}
}

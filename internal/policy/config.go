// Package policy pins the single-listener and credential boundary during reload.
package policy

import (
	"errors"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/crypto/bcrypt"
	"path/filepath"
	"reflect"
	"strings"
)

func Frozen(initial *config.Config, coreKey, configDir string) func(*config.Config) error {
	host, port, tls := initial.Host, initial.Port, initial.TLS
	plugins := initial.Plugins.Enabled
	auth := absolute(initial.AuthDir, configDir)
	return func(next *config.Config) error {
		if next == nil {
			return errors.New("embedded configuration cannot be nil")
		}
		if next.Host != host || next.Port != port || !reflect.DeepEqual(next.TLS, tls) {
			return errors.New("listener and TLS changes require a controlled restart")
		}
		if next.Pprof.Enable || next.Discovery.Enabled || next.Home.Enabled {
			return errors.New("embedded mode forbids auxiliary listeners and Home bootstrap")
		}
		if next.Plugins.Enabled != plugins {
			return errors.New("plugin enablement requires restart with a compatible build")
		}
		if absolute(next.AuthDir, configDir) != auth {
			return errors.New("credential directory is pinned for this process")
		}
		if next.RemoteManagement.AllowRemote || !next.RemoteManagement.DisableControlPanel || !next.RemoteManagement.DisableAutoUpdatePanel {
			return errors.New("embedded management isolation cannot be disabled")
		}
		value := next.RemoteManagement.SecretKey
		if value != coreKey && bcrypt.CompareHashAndPassword([]byte(value), []byte(coreKey)) != nil {
			return errors.New("internal management credential cannot be rotated through the panel")
		}
		return nil
	}
}
func absolute(path, base string) string {
	if strings.HasPrefix(path, "~") {
		return path
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}
func ValidateReleaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "https://") {
		return fmt.Errorf("release index must use HTTPS")
	}
	return nil
}

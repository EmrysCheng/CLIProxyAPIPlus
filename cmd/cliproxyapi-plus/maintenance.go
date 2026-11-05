package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type inspection struct {
	AuthDir    string `json:"auth_dir"`
	TLSEnabled bool   `json:"tls_enabled"`
	TLSCert    string `json:"tls_cert"`
	TLSKey     string `json:"tls_key"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
}

func parseNative(path string) (*config.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return config.ParseConfigBytes(data)
}
func inspectConfig(path string, w io.Writer) error {
	cfg, err := parseNative(path)
	if err != nil {
		return err
	}
	resolve := func(value string) string {
		if value == "" {
			return ""
		}
		if filepath.IsAbs(value) {
			return value
		}
		if strings.HasPrefix(value, "~/") {
			home, e := os.UserHomeDir()
			if e == nil {
				return filepath.Join(home, value[2:])
			}
		}
		return filepath.Join(filepath.Dir(path), value)
	}
	return json.NewEncoder(w).Encode(inspection{resolve(cfg.AuthDir), cfg.TLS.Enable, resolve(cfg.TLS.Cert), resolve(cfg.TLS.Key), cfg.Host, cfg.Port})
}
func adoptConfig(source, destination, dataDir, listen string) error {
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	cfg, err := config.ParseConfigBytes(raw)
	if err != nil {
		return err
	}
	if listen == "" {
		port := cfg.Port
		if port <= 0 {
			port = 8317
		}
		listen = fmt.Sprintf("127.0.0.1:%d", port)
	}
	if err := initialize(destination, dataDir, listen); err != nil {
		return err
	}
	host, port, err := parseAddress(listen)
	if err != nil {
		return err
	}
	cfg.Host = host
	cfg.Port = port
	cfg.AuthDir = filepath.Join(dataDir, "auths")
	cfg.Pprof.Enable = false
	cfg.Discovery.Enabled = false
	key, err := readCredential(filepath.Join(dataDir, "core-management.key"))
	if err != nil {
		return err
	}
	cfg.RemoteManagement.SecretKey = key
	cfg.RemoteManagement.AllowRemote = false
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.RemoteManagement.DisableAutoUpdatePanel = true
	if len(cfg.APIKeys) > 0 {
		if err := os.WriteFile(filepath.Join(dataDir, "api.key"), []byte(cfg.APIKeys[0]+"\n"), 0600); err != nil {
			return err
		}
	} else {
		api, e := readCredential(filepath.Join(dataDir, "api.key"))
		if e != nil {
			return e
		}
		cfg.APIKeys = []string{api}
	}
	if cfg.TLS.Enable {
		if !filepath.IsAbs(cfg.TLS.Cert) {
			cfg.TLS.Cert = filepath.Join(filepath.Dir(source), cfg.TLS.Cert)
		}
		if !filepath.IsAbs(cfg.TLS.Key) {
			cfg.TLS.Key = filepath.Join(filepath.Dir(source), cfg.TLS.Key)
		}
	}
	// destination was exclusively created by initialize; the source is never written.
	if err := os.WriteFile(destination, raw, 0600); err != nil {
		return err
	}
	return config.SaveConfigPreserveComments(destination, cfg)
}
func relocateConfig(path, dataDir string) error {
	cfg, err := parseNative(path)
	if err != nil {
		return err
	}
	cfg.AuthDir = filepath.Join(dataDir, "auths")
	if cfg.TLS.Enable {
		cert := filepath.Join(dataDir, "tls", "server.crt")
		key := filepath.Join(dataDir, "tls", "server.key")
		if _, e := os.Stat(cert); e != nil {
			return errors.New("restored TLS certificate is missing")
		}
		if _, e := os.Stat(key); e != nil {
			return errors.New("restored TLS private key is missing")
		}
		cfg.TLS.Cert = cert
		cfg.TLS.Key = key
	}
	return config.SaveConfigPreserveComments(path, cfg)
}
func resetAdmin(ctx context.Context, dataDir string) error {
	return manager.ResetAdmin(ctx, dataDir, os.Stdout, os.Stderr)
}

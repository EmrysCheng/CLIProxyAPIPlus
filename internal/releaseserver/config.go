// Package releaseserver serves only explicitly indexed local release artifacts.
package releaseserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	SchemaVersion int    `json:"schema_version"`
	Listen        string `json:"listen"`
	ReleaseDir    string `json:"release_dir"`
	LogFile       string `json:"log_file"`
	LogMaxSizeMB  int    `json:"log_max_size_mb"`
}

func ValidateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen must be a literal loopback IP and fixed port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	number, portErr := strconv.Atoi(port)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" || portErr != nil || number < 1 || number > 65535 {
		return errors.New("listen must use a literal loopback IP (127.0.0.1 or ::1) and port 1..65535; DNS/LAN/wildcard listeners are not supported")
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	var config Config
	file, err := os.Open(path)
	if err != nil {
		return config, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
		return config, errors.New("config must be a regular JSON file at most 16 KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16385))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("release-server config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return config, errors.New("config must contain a single JSON object, at most 16 KiB")
	}
	if config.SchemaVersion != 1 {
		return config, errors.New("unsupported release-server config schema")
	}
	if err := ValidateListen(config.Listen); err != nil {
		return config, err
	}
	if config.ReleaseDir == "" || config.LogFile == "" {
		return config, errors.New("release_dir and log_file are required")
	}
	if config.LogMaxSizeMB == 0 {
		config.LogMaxSizeMB = 10
	}
	if config.LogMaxSizeMB < 1 || config.LogMaxSizeMB > 128 {
		return config, errors.New("log_max_size_mb must be 1..128")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return config, err
	}
	for _, value := range []*string{&config.ReleaseDir, &config.LogFile} {
		if !filepath.IsAbs(*value) {
			*value = filepath.Join(filepath.Dir(absolute), *value)
		}
		*value = filepath.Clean(*value)
	}
	return config, nil
}

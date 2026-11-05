package sourceupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var sourceHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Tools struct {
	GoProxy     string `json:"go_proxy,omitempty"`
	NpmRegistry string `json:"npm_registry,omitempty"`
	Bundle      string `json:"bundle,omitempty"`
	Python      string `json:"python"`
	Go          string `json:"go"`
	Node        string `json:"node"`
	NpmCLI      string `json:"npm_cli"`
}
type ToolStatus struct {
	Origin   string            `json:"origin"`
	Ready    bool              `json:"ready"`
	Missing  []string          `json:"missing"`
	Versions map[string]string `json:"versions"`
}

func resolveTools(ctx context.Context, config string) (Tools, ToolStatus) {
	tools := Tools{}
	status := ToolStatus{Origin: "system", Versions: map[string]string{}, Missing: []string{}}
	if config != "" {
		raw, err := os.ReadFile(config)
		if err == nil {
			if len(raw) > 16384 || json.Unmarshal(raw, &tools) != nil {
				status.Missing = append(status.Missing, "valid update-tools.json")
				return tools, status
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			status.Missing = append(status.Missing, "readable update-tools.json")
			return tools, status
		}
	}
	if err := validateBuildNetwork(tools); err != nil {
		status.Missing = append(status.Missing, "valid trusted HTTPS build network settings in update-tools.json")
		return tools, status
	}
	if tools.Bundle != "" {
		if tools.Python != "" || tools.Go != "" || tools.Node != "" || tools.NpmCLI != "" {
			status.Missing = append(status.Missing, "bundle cannot be mixed with custom tool paths")
			return tools, status
		}
		directory := tools.Bundle
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(filepath.Dir(config), directory)
		}
		goProxy, npmRegistry := tools.GoProxy, tools.NpmRegistry
		directory, err := filepath.Abs(directory)
		if err == nil {
			tools, err = bundledTools(ctx, directory)
			tools.GoProxy, tools.NpmRegistry = goProxy, npmRegistry
		}
		status.Origin = "bundled"
		if err != nil {
			status.Missing = append(status.Missing, "bundled SDK integrity/platform verification failed; reinstall the complete matching package")
			return tools, status
		}
	} else if tools.Python != "" || tools.Go != "" || tools.Node != "" || tools.NpmCLI != "" {
		status.Origin = "custom"
	}
	resolve := func(value string, fallback ...string) string {
		if value != "" {
			if config != "" && !filepath.IsAbs(value) && strings.ContainsAny(value, "/\\") {
				value = filepath.Join(filepath.Dir(config), value)
			}
			fallback = []string{value}
		}
		for _, name := range fallback {
			path, err := exec.LookPath(name)
			if err == nil {
				absolute, _ := filepath.Abs(path)
				return absolute
			}
		}
		return ""
	}
	tools.Python = resolve(tools.Python, "python3", "python")
	tools.Go = resolve(tools.Go, "go")
	tools.Node = resolve(tools.Node, "node")
	probe := func(name, path string, args ...string) string {
		if path == "" {
			status.Missing = append(status.Missing, name)
			return ""
		}
		timeout, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		command := exec.CommandContext(timeout, path, args...)
		command.Env = toolEnvironment(tools)
		hideProbe(command)
		raw, err := command.CombinedOutput()
		if err != nil || len(raw) > 8192 {
			status.Missing = append(status.Missing, name)
			return ""
		}
		value := strings.TrimSpace(string(raw))
		if len(value) > 256 {
			value = value[:256]
		}
		status.Versions[name] = value
		return value
	}
	python := probe("Python", tools.Python, "-I", "-B", "--version")
	goVersion := probe("Go", tools.Go, "version")
	node := probe("Node", tools.Node, "--version")
	version := func(value, pattern string, minor, patch int) bool {
		m := regexp.MustCompile(pattern).FindStringSubmatch(value)
		if len(m) < 2 {
			return false
		}
		n, _ := strconv.Atoi(m[1])
		p := 0
		if len(m) > 2 {
			p, _ = strconv.Atoi(m[2])
		}
		return n > minor || (n == minor && p >= patch)
	}
	if python != "" && !version(python, `Python 3\.([0-9]+)(?:\.([0-9]+))?`, 11, 0) {
		status.Missing = append(status.Missing, "Python >=3.11")
	}
	if goVersion != "" && !version(goVersion, `go1\.([0-9]+)(?:\.([0-9]+))?`, 26, 10) {
		status.Missing = append(status.Missing, "Go >=1.26.10")
	}
	if node != "" && !supportedNode(node) {
		status.Missing = append(status.Missing, "Node ^22.23.4 || ^24.22.1 || >=26.12.1")
	}
	if tools.NpmCLI != "" && config != "" && !filepath.IsAbs(tools.NpmCLI) {
		tools.NpmCLI = filepath.Join(filepath.Dir(config), tools.NpmCLI)
	}
	if tools.NpmCLI == "" {
		candidates := []string{filepath.Join(filepath.Dir(tools.Node), "node_modules", "npm", "bin", "npm-cli.js")}
		if path, err := exec.LookPath("npm"); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(path), "node_modules", "npm", "bin", "npm-cli.js"))
		}
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				tools.NpmCLI = candidate
				break
			}
		}
	}
	if tools.NpmCLI == "" {
		status.Missing = append(status.Missing, "npm CLI path")
	} else {
		absolute, _ := filepath.Abs(tools.NpmCLI)
		tools.NpmCLI = absolute
		probe("npm", tools.Node, tools.NpmCLI, "--version")
	}
	status.Ready = len(status.Missing) == 0
	return tools, status
}

// Mirrors the locked Manager package.json engines. Candidate builds still have
// to satisfy any newer upstream requirements and pass the complete pipeline.
func supportedNode(value string) bool {
	match := regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`).FindStringSubmatch(value)
	if len(match) != 4 {
		return false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	atLeast := func(wantMinor, wantPatch int) bool {
		return minor > wantMinor || (minor == wantMinor && patch >= wantPatch)
	}
	return (major == 22 && atLeast(23, 4)) || (major == 24 && atLeast(22, 1)) || (major == 26 && atLeast(12, 1)) || major > 26
}

// Only child processes see these settings. Do not mutate the system environment
// or allow an old GOROOT/Node injection to replace the SDK selected above.
func toolEnvironment(tools Tools) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if (upper == "GOPROXY" && tools.GoProxy != "") || (upper == "NPM_CONFIG_REGISTRY" && tools.NpmRegistry != "") || upper == "GOROOT" || upper == "GOENV" || upper == "GOTOOLCHAIN" || upper == "GOTOOLDIR" || upper == "NODE_OPTIONS" || upper == "NODE_PATH" || strings.HasPrefix(upper, "PYTHON") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1")
	if tools.GoProxy != "" {
		env = append(env, "GOPROXY="+tools.GoProxy)
	}
	if tools.NpmRegistry != "" {
		env = append(env, "npm_config_registry="+tools.NpmRegistry)
	}
	if tools.Bundle != "" {
		env = append(env, "GOROOT="+filepath.Dir(filepath.Dir(tools.Go)), "GOENV=off", "GOTOOLCHAIN=local")
	}
	return env
}

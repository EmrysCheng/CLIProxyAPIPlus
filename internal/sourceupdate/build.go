package sourceupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"cliproxyapi-plus/internal/webupdate"
)

func (c *Controller) build(ctx context.Context, request webupdate.SourceRequest, job string, phase func(string)) (webupdate.SourceCandidate, error) {
	var result webupdate.SourceCandidate
	timeout, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	phase("checking_tools")
	tools, status := resolveTools(timeout, c.opts.ToolsFile)
	if !status.Ready {
		return result, webupdate.SourceBuildFailure("tools_unavailable", errors.New("source build tools are missing; current service was not stopped"))
	}
	phase("verifying_recipe")
	workspace := filepath.Join(job, "workspace")
	if err := copyKit(c.opts, workspace); err != nil {
		return result, webupdate.SourceBuildFailure("recipe_invalid", err)
	}
	requestFile := filepath.Join(job, "source-request.json")
	resultFile := filepath.Join(job, "build-result.json")
	gate := filepath.Join(job, "build-owned.gate")
	payload := map[string]any{"targets": request.Targets, "current": request.Current, "product_source_sha256": c.opts.SourceDigest, "upstream_lock_sha256": c.opts.LockDigest, "tools": tools, "target": runtime.GOOS + "/" + runtime.GOARCH}
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(requestFile, raw, 0600); err != nil {
		return result, err
	}
	command := exec.Command(tools.Python, "-I", "-B", "-u", filepath.Join(workspace, "scripts", "web_source_update.py"), "--request", requestFile, "--result", resultFile, "--gate", gate)
	command.Dir = workspace
	// Build code executes as the operator. Strip credential/config environment, but
	// do not pretend this is an OS sandbox for trusted upstream compilation.
	for _, entry := range toolEnvironment(tools) {
		key, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") || strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "AZURE_") || strings.HasPrefix(upper, "GOOGLE_") || strings.HasPrefix(upper, "CPA_") || strings.HasPrefix(upper, "CLIPROXY_") || upper == "PYTHONPATH" || upper == "PYTHONHOME" || upper == "GOWORK" || upper == "GOCACHE" || upper == "ZONEINFO" || upper == "WRITABLE_PATH" {
			continue
		}
		command.Env = append(command.Env, entry)
	}
	path := filepath.Dir(tools.Python) + string(os.PathListSeparator) + filepath.Dir(tools.Go) + string(os.PathListSeparator) + filepath.Dir(tools.Node) + string(os.PathListSeparator) + os.Getenv("PATH")
	for i := len(command.Env) - 1; i >= 0; i-- {
		if key, _, _ := strings.Cut(command.Env[i], "="); strings.EqualFold(key, "PATH") {
			command.Env = append(command.Env[:i], command.Env[i+1:]...)
		}
	}
	command.Env = append(command.Env, "PATH="+path, "PYTHONUTF8=1", "PYTHONDONTWRITEBYTECODE=1")
	log, err := os.OpenFile(filepath.Join(job, "source-build.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	defer log.Close()
	output := &buildOutput{writer: log, left: 64 << 20, phase: phase}
	command.Stdout = output
	command.Stderr = output
	if err := runOwned(timeout, command, gate); err != nil {
		fmt.Fprintf(log, "\nOwned build process failed: %v\n", err)
		code := output.diagnosis()
		if errors.Is(timeout.Err(), context.DeadlineExceeded) {
			code = "source_timeout"
		} else if timeout.Err() != nil {
			code = "source_cancelled"
		}
		return result, webupdate.SourceBuildFailure(code, errors.New("source staging/build/compatibility validation failed; current program and data are unchanged; review the private source-build.log"))
	}
	raw, err = os.ReadFile(resultFile)
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &result) != nil {
		return result, errors.New("source worker did not produce a valid receipt")
	}
	return result, nil
}

type buildOutput struct {
	mu           sync.Mutex
	writer       io.Writer
	left         int
	pending      string
	phase        func(string)
	failureCode  string
	lastActivity time.Time
}

func (w *buildOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(data)
	part := data
	if len(part) > w.left {
		part = part[:w.left]
	}
	if len(part) > 0 {
		if _, err := w.writer.Write(part); err != nil {
			return 0, err
		}
		w.left -= len(part)
	}
	w.pending += string(data)
	for {
		index := strings.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSpace(w.pending[:index])
		w.pending = w.pending[index+1:]
		if len(line) > 8192 {
			line = line[len(line)-8192:]
		}
		if code := webupdate.ClassifySourceFailure(line); code != "" {
			w.failureCode = code
		}
		if strings.HasPrefix(line, "PLUS_SOURCE_PHASE:") {
			step := strings.TrimPrefix(line, "PLUS_SOURCE_PHASE:")
			if webupdate.SourceStepIndex(step) >= 0 {
				w.phase(step)
			}
		}
	}
	if time.Since(w.lastActivity) >= 3*time.Second {
		w.lastActivity = time.Now()
		w.phase("activity")
	}
	if len(w.pending) > 8192 {
		w.pending = w.pending[len(w.pending)-8192:]
	}
	return n, nil
}

func (w *buildOutput) diagnosis() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if code := webupdate.ClassifySourceFailure(w.pending); code != "" {
		return code
	}
	return w.failureCode
}

package webupdate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var errChildStillAlive = errors.New("replacement exit could not be confirmed")

// ValidateActivation permits only the helper's recorded replacement instance to
// pass the maintenance marker. Ordinary starts remain blocked during migration.
func ValidateActivation(o Options, id string) error {
	op, err := readOperation(o, id)
	if err != nil {
		return err
	}
	if op.State != "restarting" {
		return errors.New("activation is not in the restart phase")
	}
	if err = ownedMarker(o.Data+".maintenance", id); err != nil {
		return err
	}
	hash, err := fileHash(o.Binary)
	if err != nil || hash != op.LaunchHash {
		return errors.New("activation executable hash mismatch")
	}
	return nil
}
func MarkReady(o Options, id string) error {
	if id == "" {
		return nil
	}
	if err := ValidateActivation(o, id); err != nil {
		return err
	}
	hash, err := fileHash(o.Binary)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(o.operationDir(id), "application-ready.json"), map[string]any{"pid": os.Getpid(), "binary_sha256": hash})
}
func ownedMarker(path, id string) error {
	if err := noLinks(path); err != nil {
		return err
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(bytes) != id {
		return errors.New("unrelated maintenance marker is present")
	}
	return nil
}
func clearMarker(o Options, id string) error {
	if err := ownedMarker(o.Data+".maintenance", id); err != nil {
		return err
	}
	return os.Remove(o.Data + ".maintenance")
}
func RunWorker(ctx context.Context, data, config, id string, recover bool) error {
	o := Options{Data: data, Config: config}
	op, err := readOperation(o, id)
	if err != nil {
		return err
	}
	o = op.Options
	if err = noLinks(o.operationDir(id)); err != nil {
		return err
	}
	if recover && (op.State == "succeeded" || op.State == "rolled_back" || op.State == "failed") {
		if errors.Is(ownedMarker(o.Data+".maintenance", id), os.ErrNotExist) {
			return nil
		}
		return clearMarker(o, id)
	}
	parentClean := false
	if !recover {
		if op.State != "awaiting_shutdown" || op.ParentPID < 1 {
			return errors.New("worker was not prepared by a running service")
		}
		if err = writeJSON(filepath.Join(o.operationDir(id), "worker-ready.json"), map[string]any{"pid": os.Getpid()}); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, 120*time.Second)
		err = waitForParent(wait, op.ParentPID)
		cancel()
		if err != nil {
			return err
		}
		var stopped struct {
			PID int `json:"pid"`
		}
		parentClean = readJSON(filepath.Join(o.operationDir(id), "parent-stopped.json"), &stopped) == nil && stopped.PID == op.ParentPID
	}
	lock, err := acquireOperationLock(o)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = ownedMarker(o.Data+".maintenance", id); err != nil {
		return err
	}
	current, err := readOperation(o, id)
	if err != nil {
		return err
	}
	op = current
	finish := func(state string, cause error) error {
		op.State = state
		if cause != nil {
			op.Error = cause.Error()
		}
		if e := saveOperation(&op); e != nil {
			return e
		}
		if state != "recovery_required" {
			if e := clearMarker(o, id); e != nil {
				return e
			}
		}
		return cause
	}
	if !recover && !parentClean {
		cause := errors.New("original shutdown was not clean; update was not installed")
		hash, e := fileHash(o.Binary)
		if e != nil || hash != op.OriginalHash {
			return finish("recovery_required", cause)
		}
		if e = launch(ctx, &op, op.FromVersion, op.OriginalHash); e != nil {
			return finish("recovery_required", errors.Join(cause, e))
		}
		return finish("failed", cause)
	}
	if recover {
		// Recovery is deliberately offline; the native DB lock in restore refuses to
		// displace any running Manager. Restore the pre-operation pair, not a binary alone.
		if len(op.Snapshot) == 0 {
			hash, e := fileHash(o.Binary)
			if e != nil || hash != op.OriginalHash {
				return finish("recovery_required", errors.New("no complete snapshot and original binary cannot be confirmed"))
			}
			return finish("failed", nil)
		}
		if err = restore(&op, op); err != nil {
			return finish("recovery_required", err)
		}
		return finish("rolled_back", nil)
	}
	op.State = "backing_up"
	if err = saveOperation(&op); err != nil {
		return err
	}
	if err = snapshot(&op); err != nil {
		cause := fmt.Errorf("backup failed before replacement: %w", err)
		if e := launch(ctx, &op, op.FromVersion, op.OriginalHash); e != nil {
			return finish("recovery_required", errors.Join(cause, e))
		}
		return finish("failed", cause)
	}
	backup := op
	if op.Kind == "rollback" {
		backup, err = readOperation(o, op.Reference)
		if err == nil {
			op.State = "restoring"
			err = saveOperation(&op)
		}
		if err == nil {
			err = restore(&op, backup)
		}
	} else {
		op.State = "installing"
		err = saveOperation(&op)
		if err == nil {
			err = replaceBinary(filepath.Join(o.operationDir(id), executableName()), o.Binary, op.TargetHash)
		}
	}
	if err == nil {
		op.State = "checking_data"
		err = saveOperation(&op)
		if err == nil {
			err = checkData(ctx, o)
		}
	}
	if err == nil {
		err = launch(ctx, &op, op.TargetVersion, op.TargetHash)
	}
	if err == nil {
		state := "succeeded"
		if op.Kind == "rollback" {
			state = "rolled_back"
		}
		return finish(state, nil)
	}
	// A failed install/rollback restores its own pre-operation snapshot. Failed
	// candidate data are retained rather than deleted, including migration output.
	cause := err
	if errors.Is(cause, errChildStillAlive) {
		return finish("recovery_required", cause)
	}
	op.State = "restoring"
	op.Error = cause.Error()
	_ = saveOperation(&op)
	if err = restore(&op, op); err != nil {
		return finish("recovery_required", errors.Join(cause, err))
	}
	if err = launch(ctx, &op, op.FromVersion, op.OriginalHash); err != nil {
		return finish("recovery_required", errors.Join(cause, err))
	}
	return finish("rolled_back", cause)
}
func checkData(ctx context.Context, o Options) error {
	check, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(check, o.Binary, "--check-data", "--data-dir", o.Data)
	hideProcess(cmd)
	// Migration output is stored only in the private transaction directory.
	log, err := os.OpenFile(filepath.Join(o.stateDir(), "maintenance.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Dir = o.WorkDir
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("offline data migration failed: %w", err)
	}
	return nil
}
func launch(ctx context.Context, op *Operation, version, hash string) error {
	op.State = "restarting"
	op.LaunchHash = hash
	if err := saveOperation(op); err != nil {
		return err
	}
	readyPath := filepath.Join(op.Options.operationDir(op.ID), "application-ready.json")
	if err := noLinks(readyPath); err != nil {
		return err
	}
	if err := os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	args := append(restartArgs(*op, hash), "--web-update-activation", op.ID)
	cmd := exec.Command(op.Options.Binary, args...)
	detachProcess(cmd)
	log, err := os.OpenFile(filepath.Join(op.Options.operationDir(op.ID), "application-console.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Dir = op.Options.WorkDir
	if err = cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	healthy, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := healthClient(op.Options)
	if err == nil {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		consecutive := 0
		for {
			select {
			case e := <-done:
				return fmt.Errorf("replacement exited before readiness: %v", e)
			case <-healthy.Done():
				err = errors.New("replacement did not become healthy before the deadline")
				goto failed
			case <-ticker.C:
				var ready struct {
					PID  int    `json:"pid"`
					Hash string `json:"binary_sha256"`
				}
				if readJSON(readyPath, &ready) != nil || ready.PID != cmd.Process.Pid || ready.Hash != hash {
					continue
				}
				if healthCheck(healthy, client, op.Options.HealthURL, version) == nil {
					consecutive++
					if consecutive >= 3 {
						return nil
					}
				} else {
					consecutive = 0
				}
			}
		}
	}
failed:
	// Only terminate a replacement child started by this helper, never an
	// administrator-selected PID. Wait for exit before restoring its SQLite files.
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		return errors.Join(err, errChildStillAlive)
	}
	return err
}
func healthClient(o Options) (*http.Client, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if o.TLSCert != "" {
		data, err := os.ReadFile(o.TLSCert)
		if err != nil {
			return nil, err
		}
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("health certificate is invalid")
		}
		expected := sha256.Sum256(block.Bytes)
		// Self-listener health requests pin the configured leaf certificate. This replaces
		// public-CA/hostname validation only for this dedicated self-listener transport.
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || sha256.Sum256(state.PeerCertificates[0].Raw) != expected {
				return errors.New("local health certificate pin mismatch")
			}
			return nil
		}}
	}
	return &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("health redirects forbidden") }}, nil
}
func healthCheck(ctx context.Context, client *http.Client, base, version string) error {
	for _, path := range []string{"/plus-api/ready", "/plus-api/version"} {
		req, err := http.NewRequestWithContext(ctx, "GET", base+path, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(req)
		if err != nil {
			return err
		}
		bytes, err := io.ReadAll(io.LimitReader(response.Body, 32769))
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || len(bytes) > 32768 {
			return errors.New("health endpoint unavailable")
		}
		if path == "/plus-api/ready" {
			var result struct {
				Ready bool `json:"ready"`
			}
			if json.Unmarshal(bytes, &result) != nil || !result.Ready {
				return errors.New("service is not ready")
			}
		} else {
			var identity binaryIdentity
			if json.Unmarshal(bytes, &identity) != nil || identity.Version != version || identity.UpdateProtocol != Protocol {
				return errors.New("running release identity mismatch")
			}
		}
	}
	return nil
}

// Hash, not product version, chooses the target source recipe: upstream updates
// can legitimately retain the same integration version.
func restartArgs(op Operation, hash string) []string {
	if hash == op.TargetHash && len(op.TargetArgs) > 0 {
		return append([]string{}, op.TargetArgs...)
	}
	return append([]string{}, op.Options.Args...)
}

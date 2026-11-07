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
	"strings"
	"time"

	manager "github.com/seakee/cpa-manager-plus/apps/manager-server/pkg/embedded"
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
	logWorker := func(level, zh, en string, args ...any) {
		ts := time.Now().UTC().Format(time.RFC3339)
		zhText := fmt.Sprintf(zh, args...)
		enText := fmt.Sprintf(en, args...)
		msg := fmt.Sprintf("[%s] [%s] [zh: %s; en: %s]\n", ts, level, zhText, enText)
		_, _ = os.Stdout.WriteString(msg)
		_ = os.Stdout.Sync()
	}
	o := Options{Data: data, Config: config}
	op, err := readOperation(o, id)
	if err != nil {
		logWorker("ERROR", "读取操作记录失败: %v", "Failed to read operation: %v", err)
		return err
	}
	o = op.Options
	if err = noLinks(o.operationDir(id)); err != nil {
		logWorker("ERROR", "操作目录校验失败: %v", "Operation directory validation failed: %v", err)
		return err
	}
	if recover {
		logWorker("INFO", "启动离线维护/回滚恢复模式 (操作ID: %s)", "Starting offline maintenance/recovery mode (operation: %s)", id)
	} else {
		logWorker("INFO", "升级辅助进程已启动 (操作ID: %s, 目标版本: %s, 父进程PID: %d)", "Update helper started (operation: %s, target version: %s, parent PID: %d)", id, op.TargetVersion, op.ParentPID)
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
			logWorker("ERROR", "主服务未准备好交接，当前状态: %s", "Main service was not prepared for handover, current state: %s", op.State)
			return errors.New("worker was not prepared by a running service")
		}
		if err = writeJSON(filepath.Join(o.operationDir(id), "worker-ready.json"), map[string]any{"pid": os.Getpid()}); err != nil {
			logWorker("ERROR", "写入 worker-ready.json 失败: %v", "Failed to write worker-ready.json: %v", err)
			return err
		}
		logWorker("INFO", "已通知主服务开始平滑停机，等待主进程退出 (PID: %d)", "Notified main service to start graceful shutdown, awaiting parent exit (PID: %d)", op.ParentPID)
		wait, cancel := context.WithTimeout(ctx, 120*time.Second)
		err = waitForParent(wait, op.ParentPID)
		cancel()
		if err != nil {
			logWorker("ERROR", "等待主进程退出超时或异常: %v", "Waiting for parent process exit timed out or failed: %v", err)
			return err
		}
		logWorker("INFO", "主进程已退出，检查平滑停止状态", "Parent process exited, verifying clean shutdown status")
		var stopped struct {
			PID int `json:"pid"`
		}
		parentClean = readJSON(filepath.Join(o.operationDir(id), "parent-stopped.json"), &stopped) == nil && stopped.PID == op.ParentPID
		if !parentClean {
			if lock, lockErr := manager.AcquireDataLock(o.Data); lockErr == nil {
				_ = lock.Close()
				parentClean = true
				logWorker("INFO", "数据库排他锁已完全释放，主服务平滑停止确认成功", "Database lock is released, confirmed clean parent shutdown")
			}
		}
	}
	lock, err := acquireOperationLock(o)
	if err != nil {
		logWorker("ERROR", "获取操作排他锁失败: %v", "Failed to acquire operation lock: %v", err)
		return err
	}
	defer lock.Close()
	if err = ownedMarker(o.Data+".maintenance", id); err != nil {
		logWorker("ERROR", "维护标记归属校验失败: %v", "Maintenance marker ownership check failed: %v", err)
		return err
	}
	current, err := readOperation(o, id)
	if err != nil {
		logWorker("ERROR", "读取最新操作状态失败: %v", "Failed to read updated operation: %v", err)
		return err
	}
	op = current
	finish := func(state string, cause error, code string) error {
		op.State = state
		if code != "" {
			op.FailureCode = code
		}
		if cause != nil {
			op.Error = cause.Error()
		}
		if e := saveOperation(&op); e != nil {
			logWorker("ERROR", "保存操作状态失败: %v", "Failed to save operation state: %v", e)
			return e
		}
		if state != "recovery_required" {
			if e := clearMarker(o, id); e != nil {
				logWorker("WARN", "清除维护标记失败: %v", "Failed to clear maintenance marker: %v", e)
				return e
			}
		}
		return cause
	}
	if !recover && !parentClean {
		logWorker("WARN", "主服务未完成平滑下线，取消升级并启动原版本保持服务运行", "Original service did not complete clean shutdown, cancelling update and resuming original version")
		cause := errors.New("original shutdown was not clean; update was not installed")
		hash, e := fileHash(o.Binary)
		if e != nil || hash != op.OriginalHash {
			logWorker("ERROR", "原二进制文件校验失败，需要离线恢复: %v", "Original binary verification failed, recovery required: %v", e)
			return finish("recovery_required", cause, "shutdown_not_clean")
		}
		if e = launch(ctx, &op, op.FromVersion, op.OriginalHash); e != nil {
			logWorker("ERROR", "启动原版本失败，需要离线恢复: %v", "Failed to launch original version, recovery required: %v", e)
			return finish("recovery_required", errors.Join(cause, e), "shutdown_not_clean")
		}
		logWorker("INFO", "已安全恢复原版本运行", "Original version resumed safely")
		return finish("failed", cause, "shutdown_not_clean")
	}
	if recover {
		logWorker("INFO", "正在执行离线快照恢复与回滚", "Executing offline snapshot restoration and rollback")
		if len(op.Snapshot) == 0 {
			hash, e := fileHash(o.Binary)
			if e != nil || hash != op.OriginalHash {
				logWorker("ERROR", "快照不完整且无法确认原二进制程序", "Incomplete snapshot and original binary cannot be confirmed")
				return finish("recovery_required", errors.New("no complete snapshot and original binary cannot be confirmed"), "rollback_failed")
			}
			return finish("failed", nil, "rollback_failed")
		}
		if err = restore(&op, op); err != nil {
			logWorker("ERROR", "离线恢复失败: %v", "Offline restoration failed: %v", err)
			return finish("recovery_required", err, "rollback_failed")
		}
		logWorker("INFO", "离线恢复与回滚成功完成", "Offline restoration and rollback completed successfully")
		return finish("rolled_back", nil, "")
	}
	op.State = "backing_up"
	if err = saveOperation(&op); err != nil {
		return err
	}
	logWorker("INFO", "开始创建升级前数据快照与备份", "Creating pre-update data snapshot and backup")
	if err = snapshot(&op); err != nil {
		logWorker("ERROR", "快照备份失败: %v", "Snapshot backup failed: %v", err)
		cause := fmt.Errorf("backup failed before replacement: %w", err)
		code := "backup_failed"
		if strings.Contains(err.Error(), "database is not offline") {
			code = "database_locked"
		}
		if e := launch(ctx, &op, op.FromVersion, op.OriginalHash); e != nil {
			logWorker("ERROR", "恢复原版本失败，需要离线恢复: %v", "Failed to resume original version, recovery required: %v", e)
			return finish("recovery_required", errors.Join(cause, e), code)
		}
		return finish("failed", cause, code)
	}
	logWorker("INFO", "数据快照与备份创建成功", "Data snapshot and backup created successfully")
	backup := op
	code := ""
	if op.Kind == "rollback" {
		backup, err = readOperation(o, op.Reference)
		if err == nil {
			op.State = "restoring"
			err = saveOperation(&op)
		}
		if err == nil {
			logWorker("INFO", "开始恢复回滚快照数据 (目标版本: %s, 引用操作: %s)", "Restoring rollback snapshot data (target version: %s, reference: %s)", backup.FromVersion, op.Reference)
			err = restore(&op, backup)
		}
		if err != nil {
			code = "rollback_failed"
			logWorker("ERROR", "回滚快照恢复失败: %v", "Rollback snapshot restore failed: %v", err)
		}
	} else {
		op.State = "installing"
		err = saveOperation(&op)
		if err == nil {
			logWorker("INFO", "开始平滑替换二进制程序 (目标版本: %s)", "Starting smooth binary replacement (target version: %s)", op.TargetVersion)
			err = replaceBinary(filepath.Join(o.operationDir(id), executableName()), o.Binary, op.TargetHash)
		}
		if err != nil {
			code = "replace_binary_failed"
			logWorker("ERROR", "二进制文件替换失败: %v", "Binary replacement failed: %v", err)
		}
	}
	if err == nil {
		op.State = "checking_data"
		err = saveOperation(&op)
		if err == nil {
			logWorker("INFO", "执行离线数据完整性与迁移检查", "Running offline data integrity and migration check")
			err = checkData(ctx, o)
		}
		if err != nil {
			code = "check_data_failed"
			logWorker("ERROR", "离线数据检查失败: %v", "Offline data check failed: %v", err)
		}
	}
	if err == nil {
		targetVer := op.TargetVersion
		targetHash := op.TargetHash
		if op.Kind == "rollback" {
			targetVer = backup.FromVersion
			targetHash = backup.OriginalHash
		}
		logWorker("INFO", "启动新程序并执行健康检查 (版本: %s)", "Launching replacement binary and verifying health (version: %s)", targetVer)
		err = launch(ctx, &op, targetVer, targetHash)
		if err != nil {
			if strings.Contains(err.Error(), "exited before readiness") {
				code = "replacement_exited"
			} else {
				code = "health_deadline_exceeded"
			}
			logWorker("ERROR", "新程序启动或健康检查失败: %v", "Replacement launch or health check failed: %v", err)
		}
	}
	if err == nil {
		state := "succeeded"
		if op.Kind == "rollback" {
			state = "rolled_back"
			logWorker("INFO", "平滑回滚成功完成，目标版本已就绪运行", "Smooth rollback completed successfully, target version is ready and running")
		} else {
			logWorker("INFO", "平滑升级成功完成，新版本已就绪运行", "Smooth upgrade completed successfully, new version is ready and running")
		}
		return finish(state, nil, "")
	}
	logWorker("WARN", "升级安装未通过 (%v)，开始自动回滚至原版本快照", "Installation failed (%v), initiating automatic rollback to original version snapshot", err)
	cause := err
	if errors.Is(cause, errChildStillAlive) {
		logWorker("ERROR", "替换子进程仍在运行，需要离线恢复", "Replacement child process is still alive, offline recovery required")
		return finish("recovery_required", cause, code)
	}
	op.State = "restoring"
	op.Error = cause.Error()
	op.FailureCode = code
	_ = saveOperation(&op)
	if err = restore(&op, op); err != nil {
		logWorker("ERROR", "恢复原版本快照失败，需要离线恢复: %v", "Failed to restore original version snapshot, recovery required: %v", err)
		return finish("recovery_required", errors.Join(cause, err), "rollback_failed")
	}
	logWorker("INFO", "原版本数据与程序恢复完成，正在启动原版本", "Original data and binary restored, launching original version")
	if err = launch(ctx, &op, op.FromVersion, op.OriginalHash); err != nil {
		logWorker("ERROR", "启动原版本失败，需要离线恢复: %v", "Failed to launch original version, recovery required: %v", err)
		return finish("recovery_required", errors.Join(cause, err), code)
	}
	logWorker("INFO", "原版本已成功启动并就绪，已安全回滚保持服务连续运行", "Original version launched and healthy, safely rolled back to maintain service continuity")
	return finish("rolled_back", cause, code)
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
	var finalErr error
	for attempt := 1; attempt <= 2; attempt++ {
		if attempt > 1 {
			time.Sleep(500 * time.Millisecond)
		}
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
		client, clientErr := healthClient(op.Options)
		if clientErr == nil {
			ticker := time.NewTicker(200 * time.Millisecond)
			consecutive := 0
			readyFound := false
			for !readyFound {
				select {
				case e := <-done:
					ticker.Stop()
					cancel()
					finalErr = fmt.Errorf("replacement exited before readiness: %v", e)
					readyFound = true
				case <-healthy.Done():
					ticker.Stop()
					cancel()
					finalErr = errors.New("replacement did not become healthy before the deadline")
					readyFound = true
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
							ticker.Stop()
							cancel()
							return nil
						}
					} else {
						consecutive = 0
					}
				}
			}
		} else {
			cancel()
			finalErr = clientErr
		}
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			return errors.Join(finalErr, errChildStillAlive)
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			break
		}
	}
	return finalErr
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

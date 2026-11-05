//go:build !windows

package webupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func hideProcess(cmd *exec.Cmd)   {}
func detachProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func waitForParent(ctx context.Context, pid int) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	// This process was directly spawned by the application; reparenting detects
	// exit without signalling a possibly reused PID.
	for os.Getppid() == pid {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func renameFile(source, destination string) error { return os.Rename(source, destination) }

func openRead(path string) (*os.File, error) { return os.Open(path) }

func trustedOwner(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
		return errors.New("update directory is owned by another untrusted account")
	}
	return nil
}

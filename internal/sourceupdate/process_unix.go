//go:build !windows

package sourceupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func hideProbe(command *exec.Cmd) {}
func runOwned(ctx context.Context, command *exec.Cmd, gate string) error {
	command.WaitDelay = 10 * time.Second
	if err := ctx.Err(); err != nil {
		return err
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(gate, []byte("owned\n"), 0600); err != nil {
		syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		command.Wait()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		wait := <-done
		return errors.Join(ctx.Err(), err, wait)
	}
}

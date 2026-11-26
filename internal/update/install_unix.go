//go:build !windows

package update

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"syscall"
)

const executableName = "bits"

func resolveExecutable(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

func runInstaller(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			err = nil
		}
		return errors.Join(ctx.Err(), err, <-done)
	}
}

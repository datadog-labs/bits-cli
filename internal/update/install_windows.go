package update

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/windowsjob"
	"golang.org/x/sys/windows"
)

const executableName = "bits.exe"

// resolveExecutable follows the current junction to the version that runs.
// filepath.EvalSymlinks fails on any path through a junction since Go 1.23.
func resolveExecutable(path string) (resolved string, err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	file, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(file)) }()
	buf := make([]uint16, windows.MAX_LONG_PATH)
	// Flags 0 ask for a normalized path with a drive letter.
	n, err := windows.GetFinalPathNameByHandle(file, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(windows.UTF16ToString(buf[:n]), `\\?\`), nil
}

func runInstaller(ctx context.Context, cmd *exec.Cmd) error {
	job, err := windowsjob.NewJob()
	if err != nil {
		return err
	}
	defer job.Close()
	if err := job.Start(cmd); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		stopErr := job.Terminate()
		return errors.Join(ctx.Err(), stopErr, <-done)
	}
}

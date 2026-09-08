//go:build linux || darwin

package exec

import (
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"syscall"
	"time"
)

// execPipeWaitDelay bounds inherited output descriptors held open by a
// descendant after the direct child exits. It is not an execution timeout.
const execPipeWaitDelay = 100 * time.Millisecond

type directExecLauncher struct{}

func defaultExecShell() string {
	return resolveExecShell(os.Getenv("SHELL"), osexec.LookPath)
}

func newDirectExecLauncher() execLauncher {
	return directExecLauncher{}
}

func (directExecLauncher) Start(plan ExecPlan, stdout, stderr io.Writer) (execProcess, error) {
	command := osexec.Command(plan.Shell, "-c", plan.Command)
	command.Dir = plan.CWD
	// A nil Stdin connects the child to the null device. A nil Env inherits
	// the ordinary parent process environment unchanged.
	command.Stdin = nil
	command.Env = nil
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = execPipeWaitDelay
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &directExecProcess{command: command}, nil
}

type directExecProcess struct {
	command *osexec.Cmd
}

func (p *directExecProcess) Wait() execProcessResult {
	err := p.command.Wait()
	state := p.command.ProcessState
	if state == nil {
		return execProcessResult{outputIncomplete: errors.Is(err, osexec.ErrWaitDelay)}
	}
	outputIncomplete := errors.Is(err, osexec.ErrWaitDelay)
	waitStatus, ok := state.Sys().(syscall.WaitStatus)
	if ok && waitStatus.Signaled() {
		return execProcessResult{
			signal:           waitStatus.Signal().String(),
			outputIncomplete: outputIncomplete,
		}
	}
	exitCode := state.ExitCode()
	if exitCode >= 0 {
		if err == nil || outputIncomplete || exitCode != 0 {
			return execProcessResult{
				exitCode:         &exitCode,
				outputIncomplete: outputIncomplete,
			}
		}
	}
	return execProcessResult{outputIncomplete: outputIncomplete}
}

func (p *directExecProcess) Terminate() error {
	return signalExecProcessGroup(p.command.Process.Pid, syscall.SIGTERM)
}

func (p *directExecProcess) Kill() error {
	return signalExecProcessGroup(p.command.Process.Pid, syscall.SIGKILL)
}

func signalExecProcessGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

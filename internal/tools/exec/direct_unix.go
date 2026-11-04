//go:build linux || darwin

package exec

import (
	"errors"
	"io"
	osexec "os/exec"
	"syscall"
	"time"
)

// execPipeWaitDelay bounds inherited output descriptors held open by a
// descendant after the direct child exits. It is not an execution timeout.
const execPipeWaitDelay = 100 * time.Millisecond

type directExecLauncher struct {
	excludedEnv []string
}

func newDirectExecLauncher(excludedEnv ...string) execLauncher {
	return directExecLauncher{excludedEnv: append([]string(nil), excludedEnv...)}
}

func (launcher directExecLauncher) Start(plan ExecPlan, stdout, stderr io.Writer) (execProcess, error) {
	command := osexec.Command(plan.Shell, "-lc", plan.Command)
	command.Dir = plan.CWD
	// A nil Stdin connects the child to the null device. A nil Env inherits
	// the ordinary parent process environment unchanged.
	command.Stdin = nil
	if len(launcher.excludedEnv) > 0 {
		command.Env = shellEnvWithoutExcludedNames(command.Environ(), launcher.excludedEnv)
	}
	command.Stdout = stdout
	command.Stderr = stderr
	// Setsid starts the command in a new session, which is also a new process
	// group led by the child (so group signalling still works). Crucially, the
	// new session has no controlling terminal: descendants cannot open
	// /dev/tty, change terminal modes, or call tcsetpgrp to take the
	// foreground away from the TUI. A plain Setpgid child stays in our session
	// and can do all of those, which leaves the TUI in a background process
	// group where its next read stops it with SIGTTIN.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
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

// Unix command output follows the UTF-8 text contract.
func decodeExecOutput(bytes []byte) string { return execOutputText(bytes) }

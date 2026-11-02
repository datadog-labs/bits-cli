//go:build windows

package exec

import (
	"errors"
	"fmt"
	"io"
	osexec "os/exec"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

type directExecLauncher struct {
	excludedEnv []string
}

func newDirectExecLauncher(excludedEnv ...string) execLauncher {
	return directExecLauncher{excludedEnv: append([]string(nil), excludedEnv...)}
}

func (launcher directExecLauncher) Start(plan ExecPlan, stdout, stderr io.Writer) (execProcess, error) {
	job, err := newExecJob()
	if err != nil {
		return nil, err
	}
	command := osexec.Command(plan.Shell, windowsExecShellArgs(plan.Shell, plan.Command)...)
	command.Dir = plan.CWD
	command.Stdin = nil
	if len(launcher.excludedEnv) > 0 {
		command.Env = shellEnvWithoutExcludedNames(command.Environ(), launcher.excludedEnv)
	}
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED}
	command.WaitDelay = execPipeWaitDelay
	if err := command.Start(); err != nil {
		job.close()
		return nil, err
	}
	// The shell must not execute user code until its descendants will inherit
	// job membership. On setup failure, reap the still-suspended process.
	if err := job.assign(command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		job.close()
		_ = command.Wait()
		return nil, fmt.Errorf("assign shell to job: %w", err)
	}
	if err := resumeExecThread(command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		job.close()
		_ = command.Wait()
		return nil, fmt.Errorf("resume shell: %w", err)
	}
	return &directExecProcess{command: command, job: job}, nil
}

type directExecProcess struct {
	command *osexec.Cmd
	job     *execJob
}

func (p *directExecProcess) Wait() execProcessResult {
	// Closing the job also stops descendants left behind by a successful shell.
	// WaitDelay bounds the wait for any output handles those descendants hold.
	defer p.job.close()
	err := p.command.Wait()
	state := p.command.ProcessState
	if state == nil {
		return execProcessResult{outputIncomplete: errors.Is(err, osexec.ErrWaitDelay)}
	}
	exitCode := state.ExitCode()
	if exitCode >= 0 {
		return execProcessResult{
			exitCode:         &exitCode,
			outputIncomplete: errors.Is(err, osexec.ErrWaitDelay),
		}
	}
	return execProcessResult{outputIncomplete: errors.Is(err, osexec.ErrWaitDelay)}
}

// Windows has no Unix-style graceful group signal for these non-console
// processes. Both cancellation stages terminate the entire job immediately.
func (p *directExecProcess) Terminate() error {
	return p.job.terminate()
}

func (p *directExecProcess) Kill() error {
	return p.job.terminate()
}

// Keep Windows pipe behavior aligned with Unix: a descendant retaining an
// inherited output handle must not block Cmd.Wait forever.
const execPipeWaitDelay = 100 * time.Millisecond

// execJob owns a non-inheritable job handle. Wait and cancellation can race,
// so closing and terminating must serialize access to the handle.
type execJob struct {
	mu     sync.Mutex
	handle windows.Handle
}

func newExecJob() (*execJob, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create execution job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("configure execution job: %w", err)
	}
	return &execJob{handle: handle}, nil
}

// assign runs only during startup, before Wait or cancellation can access j.
func (j *execJob) assign(pid int) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	return windows.AssignProcessToJobObject(j.handle, process)
}

func (j *execJob) terminate() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return nil
	}
	return windows.TerminateJobObject(j.handle, 1)
}

func (j *execJob) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle != 0 {
		_ = windows.CloseHandle(j.handle)
		j.handle = 0
	}
}

// resumeExecThread reopens the initial thread because os/exec closes its handle
// during Start. The process was created suspended and has not run user code.
// There is a small crash window between Start and job assignment in which the
// suspended process is not yet protected by KILL_ON_JOB_CLOSE.
func resumeExecThread(pid int) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		defer func() { _ = windows.CloseHandle(thread) }()
		_, err = windows.ResumeThread(thread)
		return err
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	return fmt.Errorf("no initial thread found for process %d", pid)
}

// Decode legacy native-command output only when it is not already UTF-8.
// Programs choosing an unrelated code page or mixing encodings cannot be
// identified reliably from an unlabelled byte stream.
func decodeExecOutput(bytes []byte) string {
	codePage, err := windows.GetConsoleOutputCP()
	if err != nil || codePage == 65001 {
		// CP_OEMCP asks Windows to use the system OEM code page when Bits has
		// no legacy console code page (for example, headless execution).
		codePage = 1
	}
	return windowsExecOutputText(bytes, codePage)
}

func windowsExecOutputText(bytes []byte, codePage uint32) string {
	if utf8.Valid(bytes) {
		return string(bytes)
	}
	// At most one UTF-16 code unit is needed per source byte for Windows
	// legacy code pages. The retained output is already bounded.
	wide := make([]uint16, len(bytes))
	n, err := windows.MultiByteToWideChar(codePage, 0,
		&bytes[0], int32(len(bytes)), &wide[0], int32(len(wide)))
	if err != nil {
		return execOutputText(bytes)
	}
	return string(utf16.Decode(wide[:n]))
}

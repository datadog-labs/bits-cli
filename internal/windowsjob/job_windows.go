//go:build windows

package windowsjob

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Job owns a non-inheritable job handle. Wait and cancellation can race,
// so closing and terminating must serialize access to the handle.
type Job struct {
	mu     sync.Mutex
	handle windows.Handle
	// ended records that terminate ended the job's processes.
	ended bool
}

func NewJob() (*Job, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create execution job: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(handle, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return nil, errors.Join(fmt.Errorf("configure execution job: %w", err), windows.CloseHandle(handle))
	}
	return &Job{handle: handle}, nil
}

// assign runs only during startup, before Wait or cancellation can access j.
func (j *Job) assign(pid int) (err error) {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(process)) }()
	return windows.AssignProcessToJobObject(j.handle, process)
}

func (j *Job) Terminate() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle == 0 {
		return nil
	}
	j.ended = true
	return windows.TerminateJobObject(j.handle, 1)
}

func (j *Job) Terminated() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.ended
}

func (j *Job) Close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.handle != 0 {
		if err := windows.CloseHandle(j.handle); err != nil {
			slog.Warn("close execution job", "error", err)
		}
		j.handle = 0
	}
}

// resumeExecThread reopens the initial thread because os/exec closes its handle
// during Start. The process was created suspended and has not run user code.
// There is a small crash window between Start and job assignment in which the
// suspended process is not yet protected by KILL_ON_JOB_CLOSE.
func resumeExecThread(pid int) (err error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(snapshot)) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		defer func() { err = errors.Join(err, windows.CloseHandle(thread)) }()
		_, err = windows.ResumeThread(thread)
		return err
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	return fmt.Errorf("no initial thread found for process %d", pid)
}

// Start prevents the command from running before its descendants inherit job
// membership. A parent crash before assignment can only strand a suspended child.
func (j *Job) Start(command *exec.Cmd) error {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if err := command.Start(); err != nil {
		return err
	}
	if err := j.assign(command.Process.Pid); err != nil {
		return errors.Join(fmt.Errorf("assign process to job: %w", err), command.Process.Kill(), command.Wait())
	}
	if err := resumeExecThread(command.Process.Pid); err != nil {
		return errors.Join(fmt.Errorf("resume process: %w", err), command.Process.Kill(), command.Wait())
	}
	return nil
}

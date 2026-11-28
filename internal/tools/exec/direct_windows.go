//go:build windows

package exec

import (
	"errors"
	"io"
	osexec "os/exec"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/datadog-labs/bits-cli/internal/windowsjob"
	"golang.org/x/sys/windows"
)

type directExecLauncher struct {
	excludedEnv []string
}

func newDirectExecLauncher(excludedEnv ...string) execLauncher {
	return directExecLauncher{excludedEnv: append([]string(nil), excludedEnv...)}
}

func (launcher directExecLauncher) Start(plan ExecPlan, stdout, stderr io.Writer) (execProcess, error) {
	job, err := windowsjob.NewJob()
	if err != nil {
		return nil, err
	}
	args, stdin := windowsExecShellArgs(plan.Shell, plan.Command)
	command := osexec.Command(plan.Shell, args...)
	command.Dir = plan.CWD
	// Without input for the shell, a nil Stdin connects the null device.
	command.Stdin = stdin
	if len(launcher.excludedEnv) > 0 {
		command.Env = shellEnvWithoutExcludedNames(command.Environ(), launcher.excludedEnv)
	}
	command.Stdout = stdout
	command.Stderr = stderr
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	command.WaitDelay = execPipeWaitDelay
	if err := job.Start(command); err != nil {
		job.Close()
		return nil, err
	}
	return &directExecProcess{command: command, job: job}, nil
}

type directExecProcess struct {
	command *osexec.Cmd
	job     *windowsjob.Job
}

func (p *directExecProcess) Wait() execProcessResult {
	// Closing the job also stops descendants left behind by a successful shell.
	// WaitDelay bounds the wait for any output handles those descendants hold.
	defer p.job.Close()
	err := p.command.Wait()
	outputIncomplete := errors.Is(err, osexec.ErrWaitDelay)
	state := p.command.ProcessState
	if state == nil {
		return execProcessResult{outputIncomplete: outputIncomplete}
	}
	// Exit codes are DWORDs that cmd and PowerShell show as signed: -1 and
	// -1073741819 (an access violation), not 4294967295 and 3221225477.
	if p.job.Terminated() {
		// Like a Unix signal, termination leaves no exit code of the
		// command's own; the job's is the one terminate chose.
		return execProcessResult{outputIncomplete: outputIncomplete}
	}
	exitCode := int(int32(state.Sys().(syscall.WaitStatus).ExitCode))
	return execProcessResult{exitCode: &exitCode, outputIncomplete: outputIncomplete}
}

// Windows has no Unix-style graceful group signal for these non-console
// processes. Both cancellation stages terminate the entire job immediately.
func (p *directExecProcess) Terminate() error {
	return p.job.Terminate()
}

func (p *directExecProcess) Kill() error {
	return p.job.Terminate()
}

// Keep Windows pipe behavior aligned with Unix: a descendant retaining an
// inherited output handle must not block Cmd.Wait forever.
const execPipeWaitDelay = 100 * time.Millisecond

// Decode legacy native-command output only when it is not already UTF-8.
// Programs choosing an unrelated code page or mixing encodings cannot be
// identified reliably from an unlabelled byte stream.
func decodeExecOutput(bytes []byte) string {
	return windowsExecOutputText(bytes, legacyCodePage())
}

// decodeLegacyOutput decodes bytes of a stream already known not to be UTF-8.
func decodeLegacyOutput(bytes []byte) string {
	return windowsLegacyText(bytes, legacyCodePage())
}

func legacyCodePage() uint32 {
	codePage, err := windows.GetConsoleOutputCP()
	if err != nil || codePage == 65001 {
		// CP_OEMCP asks Windows to use the system OEM code page when Bits has
		// no legacy console code page (for example, headless execution).
		codePage = 1
	}
	return codePage
}

// incompleteLegacySuffix is the length of a trailing legacy code-page
// character that more bytes could still complete.
func incompleteLegacySuffix(bytes []byte) int {
	return windowsIncompleteSuffix(bytes, legacyCodePage())
}

// windowsIncompleteSuffix is 1 when bytes end in the lead byte of a
// double-byte character (Shift-JIS, GBK, Big5, UHC), found by the trim that
// makes them decode strictly.
// NOTE: bytes invalid elsewhere fail both checks, so nothing is held; nor is
// a split 4-byte GB18030 character, which Windows decodes leniently.
func windowsIncompleteSuffix(bytes []byte, codePage uint32) int {
	if len(bytes) == 0 || decodesStrictly(bytes, codePage) || !decodesStrictly(bytes[:len(bytes)-1], codePage) {
		return 0
	}
	return 1
}

func decodesStrictly(bytes []byte, codePage uint32) bool {
	if len(bytes) == 0 {
		return true
	}
	const mbErrInvalidChars = 0x8
	n, err := windows.MultiByteToWideChar(codePage, mbErrInvalidChars, &bytes[0], int32(len(bytes)), nil, 0)
	return err == nil && n > 0
}

func windowsExecOutputText(bytes []byte, codePage uint32) string {
	if utf8.Valid(bytes) {
		return string(bytes)
	}
	return windowsLegacyText(bytes, codePage)
}

func windowsLegacyText(bytes []byte, codePage uint32) string {
	if len(bytes) == 0 {
		return ""
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

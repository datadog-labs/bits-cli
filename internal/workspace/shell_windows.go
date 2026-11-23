//go:build windows

package workspace

import (
	osexec "os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// resolveDefaultShellPath prefers PowerShell 7 and falls back to the Windows
// PowerShell installation available on supported Windows systems.
func resolveDefaultShellPath() string {
	return resolvePowerShell(osexec.LookPath)
}

func resolvePowerShell(lookPath func(string) (string, error)) string {
	// TODO(windows-exec): Add cmd.exe as a last-resort shell once its distinct
	// quoting and /D /S /C invocation contract have native integration coverage.
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		resolved, err := lookPath(candidate)
		if err != nil {
			continue
		}
		absolute, err := filepath.Abs(resolved)
		if err == nil {
			return absolute
		}
	}
	return ""
}

// detachConsole keeps a probe off the console Bits draws on, whose modes
// PowerShell would otherwise share.
func detachConsole(command *osexec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
}

//go:build windows

package exec

import (
	"io"
	osexec "os/exec"
	"path/filepath"
	"strings"
)

func supportedExecShell(shell string) bool {
	switch windowsExecShellName(shell) {
	case "pwsh", "powershell", "bash":
		return true
	default:
		return false
	}
}

func supportedExecShells() string { return "pwsh, powershell, or bash" }

// lookExecShell also finds the Bash of Git for Windows, which puts only
// Git\cmd on PATH, when no Bash is on PATH.
// NOTE: WSL's System32\bash.exe, when installed, is on PATH and wins.
func lookExecShell(shell string) (string, error) {
	resolved, err := osexec.LookPath(shell)
	if err == nil || windowsExecShellName(shell) != "bash" || filepath.Base(shell) != shell {
		return resolved, err
	}
	git, gitErr := osexec.LookPath("git")
	if gitErr != nil {
		return "", err
	}
	bash, bashErr := osexec.LookPath(filepath.Join(filepath.Dir(filepath.Dir(git)), "bin", "bash.exe"))
	if bashErr != nil {
		return "", err
	}
	return bash, nil
}

func windowsExecShellName(shell string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe")
}

// windowsExecShellArgs returns the arguments that run command and what the
// shell must read from stdin, if anything.
func windowsExecShellArgs(shell, command string) ([]string, io.Reader) {
	if windowsExecShellName(shell) == "bash" {
		// Use non-interactive command mode without login profiles, which
		// can change the requested cwd in Git Bash.
		return []string{"-c", command}, nil
	}
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", powerShellBootstrap}, strings.NewReader(command)
}

// powerShellBootstrap runs the command it reads from stdin. Passing the
// command as an argument would limit it to what fits on a command line, and
// any text around it would show up in its error positions.
//
// Windows PowerShell otherwise uses a legacy console encoding. Setting
// OutputEncoding can fail without an attached console and does not replace
// writers already redirected by .NET, so it configures both pipes directly.
// PowerShell 7 colours errors and formatted output even into a pipe; plain
// text, as its documentation promises for redirected output, keeps escape
// sequences away from the model. Native programs still write theirs.
//
// The command is its own script block, dot-sourced like -Command scripts, so
// it can lead with using or param and sees no bootstrap variables.
// Its own Out-Default writes the tables the formatter buffers before the
// exit below would discard them.
//
// The exit code is that of an exit statement, 1 after a terminating error,
// and otherwise $LASTEXITCODE: -Command would map a native command's code to
// 1, and $? is always true after a script block, whatever ran inside.
// NOTE: a cmdlet run after a failed native command keeps that command's exit
// code, and a non-terminating cmdlet error alone exits 0. A top-level
// Write-Error shows the line of the bootstrap that invoked the command.
const powerShellBootstrap = `try { [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false) } catch {}
& {
    $utf8 = [Text.UTF8Encoding]::new($false)
    $stdout = [IO.StreamWriter]::new([Console]::OpenStandardOutput(), $utf8)
    $stdout.AutoFlush = $true
    [Console]::SetOut($stdout)
    $stderr = [IO.StreamWriter]::new([Console]::OpenStandardError(), $utf8)
    $stderr.AutoFlush = $true
    [Console]::SetError($stderr)
    if ($PSStyle) {
        $PSStyle.OutputRendering = 'PlainText'
    }
}
$LASTEXITCODE = 0
. $(try { [scriptblock]::Create([IO.StreamReader]::new([Console]::OpenStandardInput(), [Text.UTF8Encoding]::new($false)).ReadToEnd()) } catch { [Console]::Error.WriteLine($_.Exception.InnerException.Message); exit 1 }) | Out-Default
exit $LASTEXITCODE
`

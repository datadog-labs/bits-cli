//go:build windows

package exec

import (
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

func windowsExecShellName(shell string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe")
}

func windowsExecShellArgs(shell, command string) []string {
	if windowsExecShellName(shell) == "bash" {
		// Use non-interactive command mode without login profiles, which
		// can change the requested cwd in Git Bash.
		return []string{"-c", command}
	}
	// Windows PowerShell otherwise uses a legacy console encoding. Setting
	// OutputEncoding can fail without an attached console and does not replace
	// writers already redirected by .NET, so configure both pipes directly.
	// Leading using or param declarations are unsupported with this prefix;
	// this is an accepted limitation for command execution.
	const utf8Output = `try {
    [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
} catch {}
& {
    $utf8 = [System.Text.UTF8Encoding]::new($false)
    $stdout = [System.IO.StreamWriter]::new([Console]::OpenStandardOutput(), $utf8)
    $stdout.AutoFlush = $true
    [Console]::SetOut($stdout)
    $stderr = [System.IO.StreamWriter]::new([Console]::OpenStandardError(), $utf8)
    $stderr.AutoFlush = $true
    [Console]::SetError($stderr)
}
`
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", utf8Output + command}
}

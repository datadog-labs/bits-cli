//go:build linux || darwin

package exec

import "path/filepath"

func supportedExecShell(shell string) bool {
	switch filepath.Base(shell) {
	case "bash", "zsh", "sh":
		return true
	default:
		return false
	}
}

func supportedExecShells() string { return "bash, zsh, or sh" }

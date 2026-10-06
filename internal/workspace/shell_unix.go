//go:build linux || darwin

package workspace

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// resolveDefaultShellPath selects the account's login shell, falling back to an
// available POSIX shell. It does not trust the parent process's SHELL variable.
func resolveDefaultShellPath() string {
	return resolveShell(accountShellPath(), osexec.LookPath)
}

func accountShellPath() string {
	if runtime.GOOS == "darwin" {
		output, err := osexec.Command("/usr/bin/dscacheutil", "-q", "user", "-a", "uid", strconv.Itoa(os.Getuid())).Output()
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(output), "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "shell: "); ok {
				return strings.TrimSpace(value)
			}
		}
		return ""
	}
	data, err := osexec.Command("getent", "passwd", strconv.Itoa(os.Getuid())).Output()
	if err != nil {
		data, err = os.ReadFile("/etc/passwd")
	}
	if err != nil {
		return ""
	}
	uid := strconv.Itoa(os.Getuid())
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[2] == uid {
			return fields[6]
		}
	}
	return ""
}

func resolveShell(accountShell string, lookPath func(string) (string, error)) string {
	candidates := []string{accountShell, "bash", "zsh", "/bin/sh"}
	if runtime.GOOS == "darwin" {
		candidates[1], candidates[2] = candidates[2], candidates[1]
	}
	for _, candidate := range candidates {
		switch filepath.Base(candidate) {
		case "bash", "zsh", "sh":
		default:
			continue
		}
		if resolved, err := lookPath(candidate); err == nil {
			if absolute, err := filepath.Abs(resolved); err == nil {
				return absolute
			}
		}
	}
	return ""
}

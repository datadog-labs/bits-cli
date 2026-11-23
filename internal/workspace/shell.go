package workspace

import (
	"context"
	"fmt"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// shellVersionTimeout bounds the probe: Windows PowerShell usually starts
// in well under a second.
const shellVersionTimeout = 2 * time.Second

// probeShellVersion returns the major.minor version of a PowerShell shell,
// which models need to tell 5.1 syntax from 7, and "" for other shells.
func probeShellVersion(shell string) (string, error) {
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(shell)), ".exe") {
	case "pwsh", "powershell":
	default:
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellVersionTimeout)
	defer cancel()
	command := osexec.CommandContext(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion.ToString()")
	detachConsole(command)
	out, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("probe %s version: %w", shell, err)
	}
	fields := strings.SplitN(strings.TrimSpace(string(out)), ".", 3)
	if len(fields) < 2 {
		return "", fmt.Errorf("probe %s version: unexpected output %q", shell, out)
	}
	for _, field := range fields[:2] {
		if _, err := strconv.ParseUint(field, 10, 16); err != nil {
			return "", fmt.Errorf("probe %s version: unexpected output %q", shell, out)
		}
	}
	return fields[0] + "." + fields[1], nil
}

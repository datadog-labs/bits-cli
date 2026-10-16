// Package browser opens links with the platform's browser launcher.
package browser

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

const openTimeout = 10 * time.Second

// Open starts the operating system's browser launcher and waits long enough to
// observe startup failures. Callers should retain the URL as a manual fallback.
func Open(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command, args := launcherCommand(runtime.GOOS, target)
	launchCtx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	return exec.CommandContext(launchCtx, command, args...).Run()
}

func launcherCommand(goos, target string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{target}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		return "xdg-open", []string{target}
	}
}

//go:build !linux && !darwin

package exec

import (
	"io"
)

type unsupportedExecLauncher struct{}

// Shell resolution is deliberately bypassed here so the platform error, not
// a usually missing Unix shell, is the typed launch failure callers observe.
func defaultExecShell() string {
	return "/bin/sh"
}

func newDirectExecLauncher() execLauncher {
	return unsupportedExecLauncher{}
}

func (unsupportedExecLauncher) Start(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
	return nil, ErrExecUnsupportedPlatform
}

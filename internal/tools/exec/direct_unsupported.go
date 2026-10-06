//go:build !linux && !darwin

package exec

import (
	"io"
)

type unsupportedExecLauncher struct{}

func newDirectExecLauncher() execLauncher {
	return unsupportedExecLauncher{}
}

func (unsupportedExecLauncher) Start(ExecPlan, io.Writer, io.Writer) (execProcess, error) {
	return nil, ErrExecUnsupportedPlatform
}

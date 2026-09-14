//go:build !linux && !darwin

package exec

import (
	"context"
	"errors"
	"testing"
)

func TestExecServiceUnsupportedPlatform(t *testing.T) {
	outcome := NewExecService().Run(context.Background(), ExecRequest{
		Command: "true",
		CWD:     t.TempDir(),
	})
	if outcome.Reason != ExecLaunchFailed || !errors.Is(outcome.Err, ErrExecUnsupportedPlatform) {
		t.Fatalf("outcome = %+v, want unsupported-platform launch failure", outcome)
	}
}

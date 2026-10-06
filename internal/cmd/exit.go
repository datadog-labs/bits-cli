package cmd

import "fmt"

const (
	ExitFailure        = 1
	ExitUsage          = 2
	ExitApprovalDenied = 3
)

// ExitError carries a process status for main to honor. No lower layer exits
// the process directly.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

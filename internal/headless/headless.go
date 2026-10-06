// Package headless defines the narrow seam between the agent engine and
// noninteractive deliveries. The engine has no dependency on this package.
package headless

import (
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

// Start carries the run-opening facts a delivery may need.
type Start struct {
	StartedAt      time.Time
	RequestedModel string
}

// Finish carries the engine's authoritative terminal state.
type Finish struct {
	EndedAt time.Time
	Result  agent.TurnResult
	Err     error
}

// Delivery streams one noninteractive run. A Consume error cancels RunTurn
// through its existing consumer-failure semantics.
type Delivery interface {
	Start(Start) error
	Consume(agent.Event) error
	Finish(Finish) error
}

// Outcome is the public terminal classification shared by deliveries and the
// command's exit mapping.
type Outcome string

const (
	OutcomeCompleted      Outcome = "completed"
	OutcomeApprovalDenied Outcome = "approval_denied"
	OutcomeFailed         Outcome = "failed"
	OutcomeCanceled       Outcome = "canceled"
	OutcomeTimedOut       Outcome = "timed_out"
)

// ClassifyTurn reports the terminal outcome. TurnResult.Outcome is
// authoritative: Denied only refines a completed turn into approval_denied.
func ClassifyTurn(result agent.TurnResult) Outcome {
	switch result.Outcome {
	case agent.TurnOutcomeCompleted:
		if result.Denied {
			return OutcomeApprovalDenied
		}
		return OutcomeCompleted
	case agent.TurnOutcomeCanceled:
		return OutcomeCanceled
	case agent.TurnOutcomeDeadlineExceeded:
		return OutcomeTimedOut
	default:
		return OutcomeFailed
	}
}

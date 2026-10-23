// Package headless defines the narrow seam between the agent engine and
// noninteractive deliveries. The engine has no dependency on this package.
package headless

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

// Start carries the run-opening facts a delivery may need.
type Start struct {
	StartedAt      time.Time
	RequestedModel string
}

// Finish carries the turn's terminal state. Blocks is the engine's final
// transcript and must be treated as read-only.
type Finish struct {
	EndedAt        time.Time
	Outcome        Outcome
	ConversationID string
	Blocks         []agent.Block
	Err            error
}

// Delivery streams one noninteractive run. A Consume error cancels the turn.
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

// Run drives one turn on an idle engine through delivery, then finishes the
// delivery once. A Consume error cancels the turn, which is still drained. The
// returned error is the turn's failure, or else the Finish error.
func Run(ctx context.Context, engine *agent.Engine, input agent.TurnInput, delivery Delivery) (Outcome, error) {
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events, unsubscribe := engine.Subscribe()
	defer unsubscribe()
	completed := false
	var consumeErr error
	run, turnErr := engine.StartTurn(turnCtx, input)
	if turnErr == nil {
		for event := range agent.Follow(events, run) {
			switch event.Kind {
			case agent.EventDone:
				completed = true
			case agent.EventError, agent.EventCanceled:
				turnErr = event.Err
			case agent.EventTranscript, agent.EventUsage, agent.EventConversation:
			}
			if consumeErr == nil {
				if consumeErr = delivery.Consume(event); consumeErr != nil {
					cancel()
				}
			}
		}
	}

	blocks := engine.Snapshot()
	outcome, err := classify(completed, denied(blocks, run), turnErr, consumeErr)
	finishErr := delivery.Finish(Finish{
		EndedAt:        time.Now(),
		Outcome:        outcome,
		ConversationID: engine.ConversationID(),
		Blocks:         blocks,
		Err:            err,
	})
	if err != nil {
		return outcome, err
	}
	return outcome, finishErr
}

// classify maps the turn's terminal event to an outcome. A cancelled turn's
// error is its context's: canceled, or timed out at the deadline.
func classify(completed, denied bool, turnErr, consumeErr error) (Outcome, error) {
	switch {
	case consumeErr != nil:
		return OutcomeFailed, fmt.Errorf("consume turn event: %w", consumeErr)
	case completed && denied:
		return OutcomeApprovalDenied, nil
	case completed:
		return OutcomeCompleted, nil
	case errors.Is(turnErr, context.DeadlineExceeded):
		return OutcomeTimedOut, turnErr
	case errors.Is(turnErr, context.Canceled):
		return OutcomeCanceled, turnErr
	default:
		return OutcomeFailed, turnErr
	}
}

// denied reports whether run denied a tool call. Run 0 started nothing.
func denied(blocks []agent.Block, run agent.RunID) bool {
	for _, block := range blocks {
		if run != 0 && block.Run == run && block.Tool != nil && block.Tool.Status == agent.ToolDenied {
			return true
		}
	}
	return false
}

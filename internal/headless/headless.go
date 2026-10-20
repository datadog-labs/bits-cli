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

var errTurnIncomplete = errors.New("turn ended without a completion or error")

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

	turnStart := len(engine.Snapshot())
	completed := false
	var turnErr, consumeErr error
	for event := range engine.StartTurn(turnCtx, input) {
		switch event.Kind {
		case agent.EventTurnDone:
			completed = true
		case agent.EventError:
			turnErr = event.Err
		case agent.EventTranscript, agent.EventUsage, agent.EventConversation:
		}
		if consumeErr == nil {
			if consumeErr = delivery.Consume(event); consumeErr != nil {
				cancel()
			}
		}
	}

	blocks := engine.Snapshot()
	outcome, err := classify(ctx, completed, denied(blocks[turnStart:]), turnErr, consumeErr)
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

func classify(ctx context.Context, completed, denied bool, turnErr, consumeErr error) (Outcome, error) {
	if consumeErr != nil {
		return OutcomeFailed, fmt.Errorf("consume turn event: %w", consumeErr)
	}
	if completed {
		if denied {
			return OutcomeApprovalDenied, nil
		}
		return OutcomeCompleted, nil
	}
	err := turnErr
	if err == nil {
		err = ctx.Err()
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimedOut, err
	case errors.Is(err, context.Canceled):
		return OutcomeCanceled, err
	case err != nil:
		return OutcomeFailed, err
	default:
		return OutcomeFailed, errTurnIncomplete
	}
}

func denied(blocks []agent.Block) bool {
	for _, block := range blocks {
		if block.Tool != nil && block.Tool.Status == agent.ToolDenied {
			return true
		}
	}
	return false
}

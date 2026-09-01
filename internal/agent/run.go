package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/DataDog/bits-cli/internal/assistant"
)

var errTurnIncomplete = errors.New("turn ended without a completion or error")

// TurnOutcome classifies how a one-turn run ended independently of error text.
type TurnOutcome string

const (
	TurnOutcomeCompleted        TurnOutcome = "completed"
	TurnOutcomeFailed           TurnOutcome = "failed"
	TurnOutcomeCanceled         TurnOutcome = "canceled"
	TurnOutcomeDeadlineExceeded TurnOutcome = "deadline_exceeded"
	TurnOutcomeConsumerFailed   TurnOutcome = "consumer_failed"
)

// TurnResult is the state retained after a turn. Failed and canceled turns can
// still contain partial blocks, usage, and a conversation ID. Blocks and Usage
// are stable snapshots owned by the engine and must be treated as read-only.
type TurnResult struct {
	Outcome        TurnOutcome
	ConversationID string
	Blocks         []Block
	Usage          *assistant.Usage
	// Denied reports a refused approval gate even when the turn completed.
	Denied bool
}

// TurnEventConsumer observes engine events in order. Returning an error cancels
// the turn; RunTurn still drains the event stream before returning.
type TurnEventConsumer func(Event) error

// RunTurn executes one user turn and its client-tool round trips. It returns
// only after the engine has finalized the result and released operation
// ownership.
func (e *Engine) RunTurn(ctx context.Context, input TurnInput, consume TurnEventConsumer) (TurnResult, error) {
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	operation := e.beginTurn(turnCtx, input)
	var consumerErr error
	for event := range operation.events {
		if consume == nil || consumerErr != nil {
			continue
		}
		if err := consume(event); err != nil {
			consumerErr = err
			cancel()
		}
	}

	completion := <-operation.completion
	result := TurnResult{
		ConversationID: completion.ConversationID,
		Blocks:         completion.Blocks,
		Usage:          completion.Usage,
		Denied:         completion.Denied,
	}

	switch {
	case consumerErr != nil:
		result.Outcome = TurnOutcomeConsumerFailed
		return result, fmt.Errorf("consume turn event: %w", consumerErr)
	case completion.Completed:
		result.Outcome = TurnOutcomeCompleted
		return result, nil
	case errors.Is(completion.Err, context.DeadlineExceeded):
		result.Outcome = TurnOutcomeDeadlineExceeded
		return result, completion.Err
	case errors.Is(completion.Err, context.Canceled):
		result.Outcome = TurnOutcomeCanceled
		return result, completion.Err
	case completion.Err != nil:
		result.Outcome = TurnOutcomeFailed
		return result, completion.Err
	default:
		result.Outcome = TurnOutcomeFailed
		return result, errTurnIncomplete
	}
}

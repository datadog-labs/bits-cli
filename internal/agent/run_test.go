package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// partialAnswerBackend streams a partial answer with usage, then waits for
// cancellation.
func partialAnswerBackend() *scriptBackend {
	message := textMsg("partial", "partial answer")
	message.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 7}}
	return &scriptBackend{
		rounds:  [][]assistant.Message{{message}},
		convID:  "conversation-after-cancel",
		started: make(chan struct{}),
		hold:    make(chan struct{}),
	}
}

// runTurnCancelledAfterStart runs a turn and cancels it once the backend has
// streamed its round.
func runTurnCancelledAfterStart(backend *scriptBackend) (TurnResult, error) {
	engine := New(backend, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-backend.started
		cancel()
	}()
	return engine.RunTurn(ctx, TurnInput{Message: "question"}, nil)
}

func TestRunTurnReturnsFinalStateAndLatestUsage(t *testing.T) {
	inputTokens, outputTokens := 2, 3
	firstUsage := textMsg("usage-1", "")
	firstUsage.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 4}}
	latestUsage := &assistant.Usage{TokensUsed: 5, InputTokens: &inputTokens, OutputTokens: &outputTokens}
	lastUsage := textMsg("usage-2", "")
	lastUsage.Results = &assistant.Results{Usage: latestUsage}
	engine := New(&scriptBackend{
		convID: "conversation-1",
		rounds: [][]assistant.Message{{textMsg("answer", "hello "), textMsg("answer", "world"), firstUsage, lastUsage}},
	}, Config{})
	result, err := engine.RunTurn(context.Background(), TurnInput{Message: "question"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != TurnOutcomeCompleted || result.ConversationID != "conversation-1" {
		t.Fatalf("result = %+v", result)
	}
	if result.Usage == nil || result.Usage.TokensUsed != 5 || *result.Usage.InputTokens != 2 || *result.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	inputTokens = 99
	if *result.Usage.InputTokens != 2 {
		t.Fatal("completion did not retain its usage snapshot")
	}
	if len(result.Blocks) != 2 || result.Blocks[1].Markdown == nil || result.Blocks[1].Markdown.Content != "hello world" || !result.Blocks[1].Complete {
		t.Fatalf("final blocks = %+v", result.Blocks)
	}
	if engine.OperationActive() {
		t.Fatal("engine operation remains active")
	}
}

func TestRunTurnCancellationPreservesPartialState(t *testing.T) {
	result, err := runTurnCancelledAfterStart(partialAnswerBackend())
	if !errors.Is(err, context.Canceled) || result.Outcome != TurnOutcomeCanceled {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if result.ConversationID != "conversation-after-cancel" || len(result.Blocks) != 2 || result.Usage == nil || result.Usage.TokensUsed != 7 {
		t.Fatalf("partial result = %+v", result)
	}
}

func TestRunTurnPreservesBackendErrorReturnedAfterCancellation(t *testing.T) {
	backendErr := errors.New("backend failed during cancellation")
	backend := &scriptBackend{convID: "conversation-before-error", started: make(chan struct{}), hold: make(chan struct{}), err: backendErr}
	result, err := runTurnCancelledAfterStart(backend)
	if !errors.Is(err, backendErr) || result.Outcome != TurnOutcomeFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if result.ConversationID != "conversation-before-error" {
		t.Fatalf("conversation ID = %q", result.ConversationID)
	}
}

func TestRunTurnDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := New(partialAnswerBackend(), Config{}).RunTurn(ctx, TurnInput{Message: "question"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || result.Outcome != TurnOutcomeDeadlineExceeded {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
}

func TestRunTurnDeniedGateThenBackendFailureFails(t *testing.T) {
	backendErr := errors.New("backend failed after denial")
	engine := New(&scriptBackend{rounds: [][]assistant.Message{{deleteGate}}, err: backendErr, failOn: 2}, Config{})
	deny := decideGates(t, func(id string) bool { return engine.Decide(id, ApprovalDeny) })
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write",
		Tools:   writeTools(t, ModeManual, true, written),
		OnDeny:  DenyContinue,
	}, func(event Event) error { deny(event); return nil })
	// Runtime failure wins; the denial evidence remains.
	if !errors.Is(err, backendErr) || result.Outcome != TurnOutcomeFailed || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
}

func TestRunTurnConsumerFailureCancelsAndDrains(t *testing.T) {
	consumerErr := errors.New("writer failed")
	engine := New(partialAnswerBackend(), Config{})
	result, err := engine.RunTurn(context.Background(), TurnInput{Message: "question"}, func(event Event) error {
		if stateHasAssistant(event) {
			return consumerErr
		}
		return nil
	})
	if !errors.Is(err, consumerErr) || result.Outcome != TurnOutcomeConsumerFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if result.Usage == nil || result.Usage.TokensUsed != 7 {
		t.Fatalf("usage lost after consumer failure: %+v", result.Usage)
	}
	if engine.OperationActive() {
		t.Fatal("engine operation remains active")
	}
}

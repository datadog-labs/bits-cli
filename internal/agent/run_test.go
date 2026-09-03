package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

type cancelRunBackend struct {
	started chan struct{}
	once    sync.Once
}

func (b *cancelRunBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	var response assistant.AssistantResponse
	message := assistant.AssistantMessage("partial", assistant.TextContent("partial answer"))
	message.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 7}}
	response.Data.Attributes.StructuredMessage = message
	if err := emit(response); err != nil {
		return "conversation-after-cancel", err
	}
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return "conversation-after-cancel", ctx.Err()
}

type errorAfterCancelRunBackend struct {
	started chan struct{}
	err     error
}

func (b *errorAfterCancelRunBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	close(b.started)
	<-ctx.Done()
	return "conversation-before-error", b.err
}

func TestRunTurnReturnsFinalStateAndLatestUsage(t *testing.T) {
	inputTokens, outputTokens := 2, 3
	firstUsage := assistant.AssistantMessage("usage-1", assistant.TextContent(""))
	firstUsage.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 4}}
	latestUsage := &assistant.Usage{TokensUsed: 5, InputTokens: &inputTokens, OutputTokens: &outputTokens}
	lastUsage := assistant.AssistantMessage("usage-2", assistant.TextContent(""))
	lastUsage.Results = &assistant.Results{Usage: latestUsage}
	engine := New(&scriptBackend{
		convID: "conversation-1",
		msgs: []assistant.Message{
			assistant.AssistantMessage("answer", assistant.TextContent("hello ")),
			assistant.AssistantMessage("answer", assistant.TextContent("world")),
			firstUsage,
			lastUsage,
		},
	}, assistant.SendOptions{})
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

func TestRunTurnReturnsPartialStateOnBackendError(t *testing.T) {
	backendErr := errors.New("stream failed")
	engine := New(&scriptBackend{
		convID: "assigned-before-error",
		err:    backendErr,
		msgs:   []assistant.Message{assistant.AssistantMessage("answer", assistant.TextContent("partial"))},
	}, assistant.SendOptions{})

	result, err := engine.RunTurn(context.Background(), TurnInput{Message: "question"}, nil)
	if !errors.Is(err, backendErr) || result.Outcome != TurnOutcomeFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if result.ConversationID != "assigned-before-error" {
		t.Fatalf("conversation ID = %q", result.ConversationID)
	}
	if len(result.Blocks) != 2 || result.Blocks[1].Markdown == nil || result.Blocks[1].Markdown.Content != "partial" || !result.Blocks[1].Complete {
		t.Fatalf("partial blocks = %+v", result.Blocks)
	}
}

func TestRunTurnCancellationDrainsAndPreservesPartialState(t *testing.T) {
	backend := &cancelRunBackend{started: make(chan struct{})}
	engine := New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan TurnResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := engine.RunTurn(ctx, TurnInput{Message: "question"}, nil)
		resultCh <- result
		errCh <- err
	}()
	<-backend.started
	cancel()
	result := <-resultCh
	err := <-errCh

	if !errors.Is(err, context.Canceled) || result.Outcome != TurnOutcomeCanceled {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if result.ConversationID != "conversation-after-cancel" || len(result.Blocks) != 2 || result.Usage == nil || result.Usage.TokensUsed != 7 {
		t.Fatalf("partial result = %+v", result)
	}
	if engine.OperationActive() {
		t.Fatal("engine operation remains active")
	}
}

func TestRunTurnPreservesBackendErrorReturnedAfterCancellation(t *testing.T) {
	backendErr := errors.New("backend failed during cancellation")
	backend := &errorAfterCancelRunBackend{started: make(chan struct{}), err: backendErr}
	engine := New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan TurnResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := engine.RunTurn(ctx, TurnInput{Message: "question"}, nil)
		resultCh <- result
		errCh <- err
	}()
	<-backend.started
	cancel()
	result := <-resultCh
	err := <-errCh

	if !errors.Is(err, backendErr) || result.Outcome != TurnOutcomeFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if result.ConversationID != "conversation-before-error" {
		t.Fatalf("conversation ID = %q", result.ConversationID)
	}
}

func TestRunTurnDeadlineExceeded(t *testing.T) {
	backend := &cancelRunBackend{started: make(chan struct{})}
	engine := New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	result, err := engine.RunTurn(ctx, TurnInput{Message: "question"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || result.Outcome != TurnOutcomeDeadlineExceeded {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
}

func TestRunTurnConsumerFailureCancelsAndDrains(t *testing.T) {
	consumerErr := errors.New("writer failed")
	backend := &cancelRunBackend{started: make(chan struct{})}
	engine := New(backend, assistant.SendOptions{})

	result, err := engine.RunTurn(context.Background(), TurnInput{Message: "question"}, func(event Event) error {
		if event.Kind == EventBlock && event.Update.Changed.Role == assistant.RoleAssistant {
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

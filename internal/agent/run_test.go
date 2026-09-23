package agent

import (
	"context"
	"errors"
	"strconv"
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

type unfinishedToolBackend struct {
	started chan struct{}
	client  bool
	once    sync.Once
}

type historicalToolBackend struct {
	started chan struct{}
	once    sync.Once
	calls   int
}

type backpressureCancelBackend struct {
	filled chan struct{}
}

func (b *backpressureCancelBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	emitMessage := func(message assistant.Message) error {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = message
		return emit(response)
	}
	if err := emitMessage(assistant.AssistantMessage("unfinished", assistant.ToolCallContent("unfinished", "search_logs", `{}`))); err != nil {
		return "conversation", err
	}

	// The user echo plus the tool call plus these 62 snapshots fill the engine's
	// 64-event buffer. The next emit then waits for either a consumer or cancel.
	for i := 0; ; i++ {
		if err := emitMessage(assistant.AssistantMessage("filler-"+strconv.Itoa(i), assistant.TextContent("filler"))); err != nil {
			return "conversation", err
		}
		if i == 61 {
			close(b.filled)
		}
	}
}

func (b *historicalToolBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	callID, toolName := "current", "search_logs"
	if b.calls == 1 {
		callID, toolName = "prior", "old_tool"
	}
	content := assistant.ToolCallContent(callID, toolName, `{}`)
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage(callID, content)
	if err := emit(response); err != nil {
		return "conversation", err
	}
	if b.calls == 1 {
		return "conversation", nil
	}
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return "conversation", ctx.Err()
}

func (b *unfinishedToolBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	var messages []assistant.Message
	if b.client {
		messages = []assistant.Message{
			assistant.AssistantMessage("started", assistant.Content{
				Type: assistant.ContentToolCallStarted,
				Tool: &assistant.ToolPayload{ToolCallID: "unfinished", ToolName: "write_file", IsClientSide: true},
			}),
			assistant.AssistantMessage("delta", assistant.Content{
				Type: assistant.ContentToolCallInputDelta,
				Tool: &assistant.ToolPayload{ToolCallID: "unfinished", PartialJSON: `{"path":"half.txt"}`},
			}),
		}
	} else {
		messages = []assistant.Message{
			assistant.AssistantMessage("server-call", assistant.ToolCallContent("unfinished", "search_logs", `{"query":"x"}`)),
		}
	}
	for _, message := range messages {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = message
		if err := emit(response); err != nil {
			return "conversation", err
		}
	}
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return "conversation", ctx.Err()
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

func TestRunTurnCancellationSettlesUnfinishedTools(t *testing.T) {
	tests := []struct {
		name   string
		client bool
	}{
		{name: "client input preview", client: true},
		{name: "server call", client: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &unfinishedToolBackend{started: make(chan struct{}), client: tt.client}
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
			if err := <-errCh; !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context canceled", err)
			}
			if result.Outcome != TurnOutcomeCanceled {
				t.Fatalf("outcome = %v, want canceled", result.Outcome)
			}
			if len(result.Blocks) != 2 || result.Blocks[1].Tool == nil {
				t.Fatalf("blocks = %+v, want one tool block", result.Blocks)
			}
			tool := result.Blocks[1].Tool
			if !tool.Cancelled || tool.Status == ToolRunning || tool.Status != ToolError {
				t.Fatalf("tool = %+v, want cancelled terminal error", tool)
			}
		})
	}
}

func TestRunTurnCancellationLeavesEarlierUnfinishedToolUntouched(t *testing.T) {
	backend := &historicalToolBackend{started: make(chan struct{})}
	engine := New(backend, assistant.SendOptions{})
	if _, err := engine.RunTurn(context.Background(), TurnInput{Message: "first"}, nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan TurnResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := engine.RunTurn(ctx, TurnInput{Message: "second"}, nil)
		resultCh <- result
		errCh <- err
	}()
	<-backend.started
	cancel()
	result := <-resultCh
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if result.Outcome != TurnOutcomeCanceled || len(result.Blocks) != 4 {
		t.Fatalf("result = %+v, want cancelled turn with four blocks", result)
	}
	if result.Blocks[1].Tool == nil || result.Blocks[1].Tool.Cancelled {
		t.Fatalf("earlier tool = %+v, want untouched", result.Blocks[1].Tool)
	}
	if result.Blocks[3].Tool == nil || !result.Blocks[3].Tool.Cancelled {
		t.Fatalf("current tool = %+v, want cancelled", result.Blocks[3].Tool)
	}
}

func TestStartTurnCancellationDeliversSettledSnapshotUnderBackpressure(t *testing.T) {
	backend := &backpressureCancelBackend{filled: make(chan struct{})}
	engine := New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	events := engine.StartTurn(ctx, TurnInput{Message: "question"})

	<-backend.filled
	cancel()

	var lastTool *ToolBlock
	for event := range events {
		if event.Kind != EventTranscript {
			continue
		}
		for _, block := range event.Transcript.Blocks {
			if block.Tool != nil && block.ID.Key == "unfinished" {
				tool := *block.Tool
				lastTool = &tool
			}
		}
	}

	if lastTool == nil || !lastTool.Cancelled || lastTool.Status != ToolError {
		t.Fatalf("final tool = %+v, want cancelled terminal error", lastTool)
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

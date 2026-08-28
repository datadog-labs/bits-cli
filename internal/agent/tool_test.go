package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

type toolTurnBackend struct {
	t           *testing.T
	toolName    string
	responses   []assistant.ClientToolResponse
	definitions []assistant.ClientTool
	calls       int
}

type batchToolBackend struct {
	t         *testing.T
	toolNames []string
	responses []assistant.ClientToolResponse
	calls     int
}

type approvalScopeBackend struct {
	t         *testing.T
	calls     int
	responses [][]assistant.ClientToolResponse
}

func (b *approvalScopeBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.calls++
	if b.calls > 1 {
		responses, ok := message.([]assistant.ClientToolResponse)
		if !ok {
			b.t.Fatalf("tool follow-up has type %T", message)
		}
		b.responses = append(b.responses, responses)
	}

	var calls []string
	switch b.calls {
	case 1:
		calls = []string{"call-a", "call-b"}
	case 2:
		calls = []string{"call-c"}
	default:
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
		return "conversation-1", emit(response)
	}

	for _, id := range calls {
		content := assistant.ToolCallContent(id, "write", `{}`)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-"+id, content)
		if err := emit(response); err != nil {
			return "conversation-1", err
		}
	}
	return "conversation-1", nil
}

func (b *batchToolBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.calls++
	if b.calls == 1 {
		for _, name := range b.toolNames {
			content := assistant.ToolCallContent("call-"+name, name, `{}`)
			content.Type = assistant.ContentClientToolCall
			var response assistant.AssistantResponse
			response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-"+name, content)
			if err := emit(response); err != nil {
				return "conversation-1", err
			}
		}
		return "conversation-1", nil
	}

	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("tool follow-up has type %T", message)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
	return "conversation-1", emit(response)
}

func (b *toolTurnBackend) Send(_ context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.definitions = opts.ClientTools
	b.calls++
	if b.calls == 1 {
		content := assistant.ToolCallContent("call-1", b.toolName, `{"value":42}`)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-message", content)
		return "conversation-1", emit(response)
	}

	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("tool follow-up has type %T", message)
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
	return "conversation-1", emit(response)
}

func TestEngineToolTurn(t *testing.T) {
	harnessErr := errors.New("tool harness failed")
	tests := []struct {
		name       string
		toolName   string
		result     ToolResult
		handlerErr error
		wantStatus assistant.ToolStatus
		wantTitle  string
		wantOutput string
		wantErr    error
	}{
		{name: "success", toolName: "calculate", result: ToolResult{Output: "42"}, wantStatus: assistant.ToolStatusSuccess, wantTitle: "calculate", wantOutput: "42"},
		{name: "expected failure", toolName: "calculate", result: ToolResult{Title: "Invalid input", Output: "value is required", IsError: true}, wantStatus: assistant.ToolStatusError, wantTitle: "Invalid input", wantOutput: "value is required"},
		{name: "unknown tool", toolName: "missing", wantStatus: assistant.ToolStatusError, wantTitle: "Unknown tool", wantOutput: "no client tool named missing is registered"},
		{name: "harness failure", toolName: "calculate", handlerErr: harnessErr, wantErr: harnessErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &toolTurnBackend{t: t, toolName: tt.toolName}
			var tools *ToolSet
			if tt.toolName != "missing" {
				var err error
				tools, err = NewToolSet(Tool{
					Definition: assistant.ClientTool{Name: "calculate"},
					Handler: func(_ context.Context, call ToolCall) (ToolResult, error) {
						if call.ID != "call-1" || call.Input != `{"value":42}` {
							t.Fatalf("tool call = %+v", call)
						}
						return tt.result, tt.handlerErr
					},
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			events := drain(New(backend, assistant.SendOptions{}).StartTurn(context.Background(), TurnInput{
				Message: "use a tool",
				Tools:   tools,
			}))
			if tt.wantErr != nil {
				if got := events[len(events)-1]; got.Kind != EventError || !errors.Is(got.Err, tt.wantErr) {
					t.Fatalf("last event = %+v", got)
				}
				if backend.calls != 1 {
					t.Fatalf("backend calls = %d, want 1", backend.calls)
				}
				return
			}

			if len(backend.responses) != 1 {
				t.Fatalf("tool responses = %d, want 1", len(backend.responses))
			}
			response := backend.responses[0]
			if response.ToolCallID != "call-1" || response.Status != tt.wantStatus || response.Title != tt.wantTitle || response.Metadata.Output != tt.wantOutput {
				t.Fatalf("tool response = %+v", response)
			}
			if len(events) == 0 || events[len(events)-1].Kind != EventTurnDone {
				t.Fatalf("turn did not complete: %v", kinds(events))
			}
		})
	}
}

func TestEngineApprovalScope(t *testing.T) {
	tests := []struct {
		name          string
		decision      ApprovalDecision
		wantDecisions int
		wantRuns      int32
		wantCalls     int
	}{
		{
			name:          "allow once prompts for sibling and next round",
			decision:      ApprovalAllowOnce,
			wantDecisions: 3,
			wantRuns:      3,
			wantCalls:     3,
		},
		{
			name:          "allow session covers sibling and next round",
			decision:      ApprovalAllowSession,
			wantDecisions: 1,
			wantRuns:      3,
			wantCalls:     3,
		},
		{
			name:          "deny aborts pending round",
			decision:      ApprovalDeny,
			wantDecisions: 1,
			wantCalls:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &approvalScopeBackend{t: t}
			var runs atomic.Int32
			tools, err := NewToolSet(Tool{
				Definition: assistant.ClientTool{Name: "write"},
				Approval: func(ToolCall) (ApprovalRequirement, bool) {
					return ApprovalRequirement{
						Key:    ApprovalKey{Tool: "write", Resource: "workspace"},
						Prompt: ApprovalPrompt{Title: "Write record?"},
					}, true
				},
				Handler: func(context.Context, ToolCall) (ToolResult, error) {
					runs.Add(1)
					return ToolResult{Title: "Wrote record", Output: "ok"}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			engine := New(backend, assistant.SendOptions{})
			events := engine.StartTurn(ctx, TurnInput{Message: "write", Tools: tools})
			decisions := 0
			decide := func(id string) {
				if !engine.Decide(id, tt.decision) {
					t.Fatalf("decision for %q was not queued", id)
				}
				decisions++
			}

			waitForToolStatus(t, events, "call-a", ToolAwaitingApproval)
			decide("call-a")
			if tt.decision == ApprovalAllowOnce {
				beforeACompleted := waitForToolStatusEvents(t, events, "call-a", ToolSuccess)
				if !hasToolStatus(beforeACompleted, "call-b", ToolAwaitingApproval) {
					t.Fatal("sibling call did not request its own approval")
				}
				if hasToolStatus(beforeACompleted, "call-b", ToolRunning) {
					t.Fatal("allow-once approval released sibling call")
				}
				decide("call-b")
				waitForToolStatus(t, events, "call-b", ToolSuccess)
				waitForToolStatus(t, events, "call-c", ToolAwaitingApproval)
				decide("call-c")
			}
			rest := drain(events)

			if decisions != tt.wantDecisions {
				t.Fatalf("decisions = %d, want %d", decisions, tt.wantDecisions)
			}
			if got := runs.Load(); got != tt.wantRuns {
				t.Fatalf("handler runs = %d, want %d", got, tt.wantRuns)
			}
			if len(rest) == 0 || rest[len(rest)-1].Kind != EventTurnDone {
				t.Fatalf("turn did not end cleanly: %v", kinds(rest))
			}
			if backend.calls != tt.wantCalls {
				t.Fatalf("backend calls = %d, want %d", backend.calls, tt.wantCalls)
			}

			if tt.decision == ApprovalDeny {
				if len(backend.responses) != 0 {
					t.Fatalf("response batches = %d, want 0", len(backend.responses))
				}
				assertToolResult(t, rest, "call-a", "Permission denied")
				assertToolResult(t, rest, "call-b", "Cancelled")
				return
			}

			if len(backend.responses) != 2 || len(backend.responses[0]) != 2 || len(backend.responses[1]) != 1 {
				t.Fatalf("response batch sizes = %v, want [2 1]", responseBatchSizes(backend.responses))
			}
		})
	}
}

func TestEngineParallelApprovalKeepsCallOrder(t *testing.T) {
	backend := &batchToolBackend{t: t, toolNames: []string{"a", "b"}}
	started := make(chan string, 2)
	finished := make(chan string, 2)
	gates := map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})}
	approval := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "shared", Resource: "workspace"}}, true
	}
	tools, err := NewToolSet(
		Tool{Definition: assistant.ClientTool{Name: "a"}, Approval: approval, Handler: gatedHandler(started, finished, gates)},
		Tool{Definition: assistant.ClientTool{Name: "b"}, Approval: approval, Handler: gatedHandler(started, finished, gates)},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, TurnInput{Message: "run both", Tools: tools})
	waitForToolStatus(t, events, "call-a", ToolAwaitingApproval)
	engine.Decide("call-a", ApprovalAllowSession)
	receiveNames(t, started, 2)
	close(gates["b"])
	if name := receiveName(t, finished); name != "b" {
		t.Fatalf("first completed tool = %q, want b", name)
	}
	close(gates["a"])
	_ = drain(events)

	if len(backend.responses) != 2 {
		t.Fatalf("tool responses = %d, want 2", len(backend.responses))
	}
	if backend.responses[0].ToolCallID != "call-a" || backend.responses[1].ToolCallID != "call-b" {
		t.Fatalf("response order = %q, %q", backend.responses[0].ToolCallID, backend.responses[1].ToolCallID)
	}
}

func TestEngineCancelRunningTool(t *testing.T) {
	backend := &batchToolBackend{t: t, toolNames: []string{"a", "b"}}
	started := make(chan string, 2)
	gate := make(chan struct{})
	tools, err := NewToolSet(
		Tool{Definition: assistant.ClientTool{Name: "a"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			started <- "a"
			<-ctx.Done()
			return ToolResult{}, ctx.Err()
		}},
		Tool{Definition: assistant.ClientTool{Name: "b"}, Handler: func(context.Context, ToolCall) (ToolResult, error) {
			started <- "b"
			<-gate
			return ToolResult{Output: "b"}, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, TurnInput{Message: "run both", Tools: tools})
	receiveNames(t, started, 2)
	engine.CancelTool("call-a")
	close(gate)
	_ = drain(events)

	if len(backend.responses) != 2 {
		t.Fatalf("tool responses = %d, want 2", len(backend.responses))
	}
	if backend.responses[0].Title != "Cancelled" || backend.responses[0].Status != assistant.ToolStatusError {
		t.Fatalf("cancelled response = %+v", backend.responses[0])
	}
	if backend.responses[1].Metadata.Output != "b" {
		t.Fatalf("completed response = %+v", backend.responses[1])
	}
}

func gatedHandler(started, finished chan<- string, gates map[string]chan struct{}) ToolHandler {
	return func(_ context.Context, call ToolCall) (ToolResult, error) {
		started <- call.Name
		<-gates[call.Name]
		finished <- call.Name
		return ToolResult{Output: call.Name}, nil
	}
}

func waitForToolStatus(t *testing.T, events <-chan Event, id string, status ToolStatus) {
	t.Helper()
	_ = waitForToolStatusEvents(t, events, id, status)
}

func waitForToolStatusEvents(t *testing.T, events <-chan Event, id string, status ToolStatus) []Event {
	t.Helper()
	var received []Event
	for event := range events {
		received = append(received, event)
		if event.Kind == EventBlock && event.Update.Changed.ToolCallID() == id && event.Update.Changed.Tool.Status == status {
			return received
		}
	}
	t.Fatalf("tool %q never reached status %d", id, status)
	return nil
}

func hasToolStatus(events []Event, id string, status ToolStatus) bool {
	for _, event := range events {
		if event.Kind == EventBlock && event.Update.Changed.ToolCallID() == id && event.Update.Changed.Tool.Status == status {
			return true
		}
	}
	return false
}

func assertToolResult(t *testing.T, events []Event, id, title string) {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		block := events[i].Update.Changed
		if block.ToolCallID() != id {
			continue
		}
		if block.Tool == nil || block.Tool.Status != ToolError || block.Tool.Title != title {
			t.Fatalf("tool %q result = %+v, want error titled %q", id, block.Tool, title)
		}
		return
	}
	t.Fatalf("tool %q has no result event", id)
}

func responseBatchSizes(batches [][]assistant.ClientToolResponse) []int {
	sizes := make([]int, len(batches))
	for i, batch := range batches {
		sizes[i] = len(batch)
	}
	return sizes
}

func receiveNames(t *testing.T, names <-chan string, count int) {
	t.Helper()
	for range count {
		receiveName(t, names)
	}
}

func receiveName(t *testing.T, names <-chan string) string {
	t.Helper()
	select {
	case name := <-names:
		return name
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tool")
		return ""
	}
}

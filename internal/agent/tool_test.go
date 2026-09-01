package agent

import (
	"context"
	"errors"
	"strings"
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

	// A tool without an approval gate must behave identically in both modes.
	for _, mode := range []ApprovalMode{ModeAllowAll, ModeGated} {
		for _, tt := range tests {
			t.Run(string(mode)+"/"+tt.name, func(t *testing.T) {
				backend := &toolTurnBackend{t: t, toolName: tt.toolName}
				var tools *ToolSet
				if tt.toolName != "missing" {
					var err error
					tools, err = NewToolSet(mode, Tool{
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
			// Deny: the denial is answered, then siblings and follow-up calls cancel.
			name:          "deny aborts pending round",
			decision:      ApprovalDeny,
			wantDecisions: 1,
			wantRuns:      0,
			wantCalls:     3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &approvalScopeBackend{t: t}
			var runs atomic.Int32
			tools, err := NewToolSet(ModeGated, Tool{
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
			// Interactive surface: stop after the denial is answered on the wire.
			events := engine.StartTurn(ctx, TurnInput{Message: "write", Tools: tools})
			decisions := 0
			decide := func(id string) {
				if !engine.Decide(id, tt.decision) {
					t.Fatalf("decision for %q was not queued", id)
				}
				decisions++
			}

			var all []Event
			all = append(all, waitForToolStatusEvents(t, events, "call-a", ToolAwaitingApproval)...)
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
				all = append(all, waitForToolStatusEvents(t, events, "call-c", ToolAwaitingApproval)...)
				decide("call-c")
			}
			// ApprovalDeny aborts right after call-a's answer; a session grant
			// decides later gates on its own. Draining ends both cases.
			rest := drain(events)
			all = append(all, rest...)

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
				// Denial and sibling answer first; the follow-up drains as cancelled.
				if len(backend.responses) != 2 || len(backend.responses[0]) != 2 || len(backend.responses[1]) != 1 {
					t.Fatalf("response batch sizes = %v, want [2 1]", responseBatchSizes(backend.responses))
				}
				if response := backend.responses[0][0]; response.ToolCallID != "call-a" || response.Status != assistant.ToolStatusError || response.Metadata.Output != "local execution was denied by the user" {
					t.Fatalf("denial response = %+v, want the typed error for call-a", response)
				}
				if response := backend.responses[0][1]; response.ToolCallID != "call-b" || response.Status != assistant.ToolStatusError {
					t.Fatalf("sibling response = %+v, want the cancelled result sent on the wire", response)
				}
				if response := backend.responses[1][0]; response.ToolCallID != "call-c" || response.Status != assistant.ToolStatusError || response.Metadata.Output != "tool execution was cancelled by the user" {
					t.Fatalf("follow-up response = %+v, want the cancelled result sent on the wire", response)
				}
				assertToolResult(t, all, "call-a", "Permission denied")
				assertToolResult(t, all, "call-b", "Cancelled")
				// The abort happened before the follow-up round: call-c never decides.
				if hasToolStatus(all, "call-c", ToolAwaitingApproval) {
					t.Fatal("aborted round still processed the model's follow-up round")
				}
				return
			}

			if len(backend.responses) != 2 || len(backend.responses[0]) != 2 || len(backend.responses[1]) != 1 {
				t.Fatalf("response batch sizes = %v, want [2 1]", responseBatchSizes(backend.responses))
			}
		})
	}
}

func TestNewToolSetRejectsInvalidApprovalMode(t *testing.T) {
	for _, mode := range []ApprovalMode{"", "ask", "gated-x"} {
		set, err := NewToolSet(mode, Tool{
			Definition: assistant.ClientTool{Name: "calculate"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
		})
		if err == nil || set != nil {
			t.Fatalf("NewToolSet(%q) = (%v, %v), want an invalid-mode error", mode, set, err)
		}
		if !strings.Contains(err.Error(), "allow-all") || !strings.Contains(err.Error(), "gated") {
			t.Fatalf("NewToolSet(%q) error %q does not list the valid modes", mode, err)
		}
	}
}

// The server-injected gate name is reserved; a registered tool with that
// name would be silently intercepted by the engine.
func TestNewToolSetRejectsApprovalRequestName(t *testing.T) {
	set, err := NewToolSet(ModeAllowAll, Tool{
		Definition: assistant.ClientTool{Name: ApprovalRequestTool},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err == nil || set != nil {
		t.Fatalf("NewToolSet(%q) = (%v, %v), want an error", ApprovalRequestTool, set, err)
	}
	if !strings.Contains(err.Error(), "approval gate") {
		t.Fatalf("error %q does not name the reserved gate", err)
	}
}

func TestToolSetApprovalModeGatesPolicyConsultation(t *testing.T) {
	var policyCalls atomic.Int32
	declaredGate := func(ToolCall) (ApprovalRequirement, bool) {
		policyCalls.Add(1)
		return ApprovalRequirement{
			Key:    ApprovalKey{Tool: "write", Resource: "workspace"},
			Prompt: ApprovalPrompt{Title: "Write record?"},
		}, true
	}
	newSet := func(mode ApprovalMode) *ToolSet {
		set, err := NewToolSet(mode,
			Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }},
			Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: declaredGate, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }},
		)
		if err != nil {
			t.Fatal(err)
		}
		return set
	}

	// In gated mode the declared gate is consulted and returned.
	requirement, needs := newSet(ModeGated).Approval(ToolCall{Name: "write"})
	if !needs || requirement.Prompt.Title != "Write record?" || requirement.Key.Tool != "write" {
		t.Fatalf("gated Approval(write) = (%+v, %v), want the declared gate", requirement, needs)
	}
	if got := policyCalls.Load(); got != 1 {
		t.Fatalf("policy calls after gated consultation = %d, want 1", got)
	}

	// In allow-all mode the gate is suppressed without consulting the policy.
	requirement, needs = newSet(ModeAllowAll).Approval(ToolCall{Name: "write"})
	if needs {
		t.Fatal("allow-all Approval(write) reported a gate")
	}
	if requirement != (ApprovalRequirement{}) {
		t.Fatalf("allow-all Approval(write) = %+v, want a zero requirement", requirement)
	}
	if got := policyCalls.Load(); got != 1 {
		t.Fatalf("policy calls after allow-all consultation = %d, want 1; the mode must suppress the gate without re-declaring it", got)
	}

	// Tools without a declared gate report none in both modes, as do calls to
	// tools the set does not register (e.g. the server-injected approval_request).
	for _, mode := range []ApprovalMode{ModeAllowAll, ModeGated} {
		set := newSet(mode)
		if _, needs := set.Approval(ToolCall{Name: "read"}); needs {
			t.Fatalf("%s Approval(read) reported a gate", mode)
		}
		if _, needs := set.Approval(ToolCall{Name: "unknown"}); needs {
			t.Fatalf("%s Approval(unknown) reported a gate", mode)
		}
	}
}

func TestEngineAllowAllSkipsApprovalGates(t *testing.T) {
	backend := &approvalScopeBackend{t: t}
	var runs atomic.Int32
	tools, err := NewToolSet(ModeAllowAll, Tool{
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
	events := drain(New(backend, assistant.SendOptions{}).StartTurn(ctx, TurnInput{Message: "write", Tools: tools}))

	// No Decide is ever issued, yet every call runs and the turn completes.
	if got := runs.Load(); got != 3 {
		t.Fatalf("handler runs = %d, want 3", got)
	}
	for _, id := range []string{"call-a", "call-b", "call-c"} {
		if hasToolStatus(events, id, ToolAwaitingApproval) {
			t.Fatalf("tool %q requested approval in allow-all mode", id)
		}
		if !hasToolStatus(events, id, ToolSuccess) {
			t.Fatalf("tool %q never completed successfully in allow-all mode", id)
		}
	}
	if len(events) == 0 || events[len(events)-1].Kind != EventTurnDone {
		t.Fatalf("turn did not complete: %v", kinds(events))
	}
	if len(backend.responses) != 2 || len(backend.responses[0]) != 2 || len(backend.responses[1]) != 1 {
		t.Fatalf("response batch sizes = %v, want [2 1]", responseBatchSizes(backend.responses))
	}
}

// In a mixed toolset, gated mode gates the gated tool while the ungated
// sibling runs without any decision.
func TestEngineGatedToolsetLeavesUngatedToolsUnblocked(t *testing.T) {
	backend := &batchToolBackend{t: t, toolNames: []string{"read", "write"}}
	started := make(chan string, 2)
	gate := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
	}
	handler := func(_ context.Context, call ToolCall) (ToolResult, error) {
		started <- call.Name
		return ToolResult{Output: call.Name}, nil
	}
	tools, err := NewToolSet(ModeGated,
		Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: handler},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: gate, Handler: handler},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, TurnInput{Message: "run both", Tools: tools})

	// The ungated tool runs immediately, before any decision is made.
	if name := receiveName(t, started); name != "read" {
		t.Fatalf("ungated tool did not start before a decision: first started = %q", name)
	}
	waitForToolStatus(t, events, "call-write", ToolAwaitingApproval)
	if !engine.Decide("call-write", ApprovalAllowSession) {
		t.Fatal("decision for call-write was not queued")
	}
	if name := receiveName(t, started); name != "write" {
		t.Fatalf("second started tool = %q, want write", name)
	}
	_ = drain(events)

	if len(backend.responses) != 2 {
		t.Fatalf("tool responses = %d, want 2", len(backend.responses))
	}
	if backend.responses[0].ToolCallID != "call-read" || backend.responses[1].ToolCallID != "call-write" {
		t.Fatalf("response order = %q, %q", backend.responses[0].ToolCallID, backend.responses[1].ToolCallID)
	}
}

// The same mixed toolset in allow-all mode: no tool awaits a decision, and the
// mode leaves per-tool behavior and response order untouched.
func TestEngineAllowAllRunsMixedToolsetWithoutDecisions(t *testing.T) {
	backend := &batchToolBackend{t: t, toolNames: []string{"read", "write"}}
	started := make(chan string, 2)
	gate := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
	}
	handler := func(_ context.Context, call ToolCall) (ToolResult, error) {
		started <- call.Name
		return ToolResult{Output: call.Name}, nil
	}
	tools, err := NewToolSet(ModeAllowAll,
		Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: handler},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: gate, Handler: handler},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events := drain(New(backend, assistant.SendOptions{}).StartTurn(ctx, TurnInput{Message: "run both", Tools: tools}))

	// Every tool runs with no decision; handler start order is scheduler-
	// dependent, so only the completed set is asserted here.
	receiveNames(t, started, 2)
	for _, id := range []string{"call-read", "call-write"} {
		if hasToolStatus(events, id, ToolAwaitingApproval) {
			t.Fatalf("tool %q requested approval in allow-all mode", id)
		}
		if !hasToolStatus(events, id, ToolSuccess) {
			t.Fatalf("tool %q never completed successfully", id)
		}
	}
	if len(backend.responses) != 2 {
		t.Fatalf("tool responses = %d, want 2", len(backend.responses))
	}
	if backend.responses[0].ToolCallID != "call-read" || backend.responses[1].ToolCallID != "call-write" {
		t.Fatalf("response order = %q, %q", backend.responses[0].ToolCallID, backend.responses[1].ToolCallID)
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
	tools, err := NewToolSet(ModeGated,
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
	tools, err := NewToolSet(ModeGated,
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

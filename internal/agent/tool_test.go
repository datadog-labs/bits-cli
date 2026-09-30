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
	for _, mode := range []PermissionsMode{ModeSkipPermissions, ModeManual} {
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
			tools, err := NewToolSet(ModeManual, Tool{
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
			events := engine.StartTurn(ctx, TurnInput{Message: "write", Tools: tools, OnDeny: DenyStop})
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
				assertToolResult(t, all, "call-a", "Permission denied", ToolDenied)
				assertToolResult(t, all, "call-b", "Cancelled", ToolCancelled)
				if !hasDeniedToolBlock(t, all, "call-a") {
					t.Fatal("tool call-a denial was not recorded as a typed outcome")
				}
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

func TestNewToolSetRejectsInvalidPermissionsMode(t *testing.T) {
	for _, mode := range []PermissionsMode{"", "ask", "allow-all", "gated"} {
		set, err := NewToolSet(mode, Tool{
			Definition: assistant.ClientTool{Name: "calculate"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
		})
		if err == nil || set != nil {
			t.Fatalf("NewToolSet(%q) = (%v, %v), want an invalid-mode error", mode, set, err)
		}
		if !strings.Contains(err.Error(), "manual") || !strings.Contains(err.Error(), "skip-permissions") {
			t.Fatalf("NewToolSet(%q) error %q does not list the valid modes", mode, err)
		}
	}
}

// The server-injected gate name is reserved; a registered tool with that
// name would be silently intercepted by the engine.
func TestNewToolSetRejectsApprovalRequestName(t *testing.T) {
	set, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: assistant.ApprovalRequestTool},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err == nil || set != nil {
		t.Fatalf("NewToolSet(%q) = (%v, %v), want an error", assistant.ApprovalRequestTool, set, err)
	}
	if !strings.Contains(err.Error(), "approval gate") {
		t.Fatalf("error %q does not name the reserved gate", err)
	}
}

func TestToolSetReduceInput(t *testing.T) {
	type contextKey struct{}
	type renderState struct {
		input string
		prior any
	}
	var gotContext context.Context
	var gotUpdate ToolInputUpdate
	var gotPrior any
	set, err := NewToolSet(ModeSkipPermissions,
		Tool{
			Definition: assistant.ClientTool{Name: "reduce"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
			InputReducer: func(ctx context.Context, update ToolInputUpdate, prior any) any {
				gotContext = ctx
				gotUpdate = update
				gotPrior = prior
				return renderState{input: update.RawPrefix, prior: prior}
			},
		},
		Tool{
			Definition: assistant.ClientTool{Name: "ordinary"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), contextKey{}, "value")
	prior := &renderState{input: "old"}
	update := ToolInputUpdate{
		ToolCallID:       "call-1",
		Name:             "reduce",
		Delta:            "fragment",
		RawPrefix:        `{"path":"file.txt"}`,
		PreviewTruncated: true,
		FinalInput:       `{"path":"file.txt","content":"done"}`,
		HasFinalInput:    true,
	}
	state, exists := set.ReduceInput(ctx, update, prior)
	if !exists {
		t.Fatal("ReduceInput reported no reducer")
	}
	if gotContext != ctx || gotUpdate != update || gotPrior != prior {
		t.Fatalf("reducer arguments = (%v, %+v, %v), want (%v, %+v, %v)", gotContext, gotUpdate, gotPrior, ctx, update, prior)
	}
	gotState, ok := state.(renderState)
	if !ok || gotState.input != update.RawPrefix || gotState.prior != prior {
		t.Fatalf("reduced state = %#v, want state built from update and prior", state)
	}

	if state, exists := set.ReduceInput(ctx, ToolInputUpdate{Name: "ordinary"}, prior); exists || state != nil {
		t.Fatalf("ordinary tool ReduceInput = (%#v, %v), want (nil, false)", state, exists)
	}
	if state, exists := set.ReduceInput(ctx, ToolInputUpdate{Name: "missing"}, prior); exists || state != nil {
		t.Fatalf("unknown tool ReduceInput = (%#v, %v), want (nil, false)", state, exists)
	}
	var nilSet *ToolSet
	if state, exists := nilSet.ReduceInput(ctx, update, prior); exists || state != nil {
		t.Fatalf("nil ToolSet ReduceInput = (%#v, %v), want (nil, false)", state, exists)
	}
}

func TestToolSetReduceInputAllowsNilState(t *testing.T) {
	set, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "reduce"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
		InputReducer: func(context.Context, ToolInputUpdate, any) any {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, exists := set.ReduceInput(context.Background(), ToolInputUpdate{Name: "reduce"}, "prior")
	if !exists || state != nil {
		t.Fatalf("nil reducer state = (%#v, %v), want (nil, true)", state, exists)
	}
}

func TestToolSetNeedsStreamedInput(t *testing.T) {
	var nilSet *ToolSet
	if nilSet.NeedsStreamedInput() {
		t.Fatal("nil ToolSet needs streamed input")
	}
	ordinary, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "ordinary"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.NeedsStreamedInput() {
		t.Fatal("ordinary-only ToolSet needs streamed input")
	}
	withReducer, err := NewToolSet(ModeSkipPermissions,
		Tool{
			Definition: assistant.ClientTool{Name: "ordinary"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
		},
		Tool{
			Definition: assistant.ClientTool{Name: "reduce"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
			InputReducer: func(context.Context, ToolInputUpdate, any) any {
				return nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !withReducer.NeedsStreamedInput() {
		t.Fatal("ToolSet with reducer does not need streamed input")
	}
}

func TestToolSetNormalizeResult(t *testing.T) {
	set, err := NewToolSet(ModeSkipPermissions,
		Tool{
			Definition: assistant.ClientTool{Name: "reduce"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
			InputReducer: func(context.Context, ToolInputUpdate, any) any {
				return nil
			},
		},
		Tool{
			Definition: assistant.ClientTool{Name: "ordinary"},
			Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	cleared := set.NormalizeResult(ToolCall{Name: "reduce"}, ToolResult{Output: "denied"})
	if cleared.RenderState == nil || cleared.RenderState.State != nil {
		t.Fatalf("omitted reducer result RenderState = %#v, want explicit clear", cleared.RenderState)
	}

	state := &RenderStateUpdate{State: "final"}
	preserved := set.NormalizeResult(ToolCall{Name: "reduce"}, ToolResult{RenderState: state})
	if preserved.RenderState != state {
		t.Fatalf("explicit reducer result RenderState = %#v, want same update", preserved.RenderState)
	}
	clearedExplicitly := set.NormalizeResult(ToolCall{Name: "reduce"}, ToolResult{RenderState: ClearRenderState()})
	if clearedExplicitly.RenderState == nil || clearedExplicitly.RenderState.State != nil {
		t.Fatalf("explicit clear RenderState = %#v, want clear", clearedExplicitly.RenderState)
	}

	ordinaryResult := ToolResult{}
	if got := set.NormalizeResult(ToolCall{Name: "ordinary"}, ordinaryResult); got.RenderState != nil {
		t.Fatalf("ordinary result RenderState = %#v, want nil", got.RenderState)
	}
	if got := set.NormalizeResult(ToolCall{Name: "missing"}, ordinaryResult); got.RenderState != nil {
		t.Fatalf("unknown result RenderState = %#v, want nil", got.RenderState)
	}
	var nilSet *ToolSet
	if got := nilSet.NormalizeResult(ToolCall{Name: "reduce"}, ordinaryResult); got.RenderState != nil {
		t.Fatalf("nil ToolSet result RenderState = %#v, want nil", got.RenderState)
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
	newSet := func(mode PermissionsMode) *ToolSet {
		set, err := NewToolSet(mode,
			Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }},
			Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: declaredGate, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }},
		)
		if err != nil {
			t.Fatal(err)
		}
		return set
	}

	// In manual mode the declared gate is consulted and returned.
	requirement, needs := newSet(ModeManual).Approval(ToolCall{Name: "write"})
	if !needs || requirement.Prompt.Title != "Write record?" || requirement.Key.Tool != "write" {
		t.Fatalf("manual Approval(write) = (%+v, %v), want the declared gate", requirement, needs)
	}
	if got := policyCalls.Load(); got != 1 {
		t.Fatalf("policy calls after manual consultation = %d, want 1", got)
	}

	// In skip-permissions mode the gate is suppressed without consulting the policy.
	requirement, needs = newSet(ModeSkipPermissions).Approval(ToolCall{Name: "write"})
	if needs {
		t.Fatal("skip-permissions Approval(write) reported a gate")
	}
	if requirement != (ApprovalRequirement{}) {
		t.Fatalf("skip-permissions Approval(write) = %+v, want a zero requirement", requirement)
	}
	if got := policyCalls.Load(); got != 1 {
		t.Fatalf("policy calls after skip-permissions consultation = %d, want 1; the mode must suppress the gate without re-declaring it", got)
	}

	// Tools without a declared gate report none in both modes, as do calls to
	// tools the set does not register. The server-injected approval_request is
	// the one protocol-level exception: manual mode exposes it as an approval.
	for _, mode := range []PermissionsMode{ModeSkipPermissions, ModeManual} {
		set := newSet(mode)
		if _, needs := set.Approval(ToolCall{Name: "read"}); needs {
			t.Fatalf("%s Approval(read) reported a gate", mode)
		}
		if _, needs := set.Approval(ToolCall{Name: "unknown"}); needs {
			t.Fatalf("%s Approval(unknown) reported a gate", mode)
		}
	}
	requirement, needs = newSet(ModeManual).Approval(ToolCall{
		ID:    "gate-1",
		Name:  assistant.ApprovalRequestTool,
		Input: `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-1"}`,
	})
	if !needs || requirement.Key != (ApprovalKey{Tool: assistant.ApprovalRequestTool, Resource: "delete_dashboard"}) || requirement.Prompt.Title != `Allow the assistant to perform the "delete_dashboard" action?` || requirement.Prompt.Detail != "tool: delete_dashboard" {
		t.Fatalf("manual Approval(approval_request) = (%+v, %v), want the server gate", requirement, needs)
	}
}

func TestGateSnapshotGovernsOneGateEvaluation(t *testing.T) {
	set, err := NewToolSet(ModeManual, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Approval: func(ToolCall) (ApprovalRequirement, bool) {
			return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
		},
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	serverGate := ToolCall{
		ID:    "gate-1",
		Name:  assistant.ApprovalRequestTool,
		Input: `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-1"}`,
	}

	manual := set.snapshotPermissions()
	if err := set.SetPermissionsMode(ModeSkipPermissions); err != nil {
		t.Fatal(err)
	}
	if manual.approvesServerGate() {
		t.Fatal("manual snapshot approved the server gate after the set switched to skip-permissions")
	}
	if _, needs := manual.approval(serverGate); !needs {
		t.Fatal("manual snapshot dropped the server-gate approval after the set switched to skip-permissions")
	}
	if _, needs := manual.approval(ToolCall{Name: "write"}); !needs {
		t.Fatal("manual snapshot dropped the write-tool gate after the set switched to skip-permissions")
	}

	skip := set.snapshotPermissions()
	if err := set.SetPermissionsMode(ModeManual); err != nil {
		t.Fatal(err)
	}
	if !skip.approvesServerGate() {
		t.Fatal("skip-permissions snapshot denied the server gate after the set switched to manual")
	}
	if _, needs := skip.approval(ToolCall{Name: "write"}); needs {
		t.Fatal("skip-permissions snapshot re-declared the write-tool gate after the set switched to manual")
	}
}

func TestEngineSkipPermissionsSkipsApprovalGates(t *testing.T) {
	backend := &approvalScopeBackend{t: t}
	var runs atomic.Int32
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
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
			t.Fatalf("tool %q requested approval in skip-permissions mode", id)
		}
		if !hasToolStatus(events, id, ToolSuccess) {
			t.Fatalf("tool %q never completed successfully in skip-permissions mode", id)
		}
	}
	if len(events) == 0 || events[len(events)-1].Kind != EventTurnDone {
		t.Fatalf("turn did not complete: %v", kinds(events))
	}
	if len(backend.responses) != 2 || len(backend.responses[0]) != 2 || len(backend.responses[1]) != 1 {
		t.Fatalf("response batch sizes = %v, want [2 1]", responseBatchSizes(backend.responses))
	}
}

// In a mixed toolset, manual mode gates the gated tool while the ungated
// sibling runs without any decision.
func TestEngineManualModeLeavesUngatedToolsUnblocked(t *testing.T) {
	backend := &batchToolBackend{t: t, toolNames: []string{"read", "write"}}
	started := make(chan string, 2)
	gate := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
	}
	handler := func(_ context.Context, call ToolCall) (ToolResult, error) {
		started <- call.Name
		return ToolResult{Output: call.Name}, nil
	}
	tools, err := NewToolSet(ModeManual,
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

// The same mixed toolset in skip-permissions mode: no tool awaits a decision,
// and the mode leaves per-tool behavior and response order untouched.
func TestEngineSkipPermissionsRunsMixedToolsetWithoutDecisions(t *testing.T) {
	backend := &batchToolBackend{t: t, toolNames: []string{"read", "write"}}
	started := make(chan string, 2)
	gate := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
	}
	handler := func(_ context.Context, call ToolCall) (ToolResult, error) {
		started <- call.Name
		return ToolResult{Output: call.Name}, nil
	}
	tools, err := NewToolSet(ModeSkipPermissions,
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
			t.Fatalf("tool %q requested approval in skip-permissions mode", id)
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
	tools, err := NewToolSet(ModeManual,
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
	tools, err := NewToolSet(ModeManual,
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

// StopTools answers every unresolved call in the round as cancelled, persists
// that batch, and ends the turn without folding the backend's follow-up.
func TestEngineStopToolsAnswersRoundAndEndsTurn(t *testing.T) {
	backend := &batchToolBackend{t: t, toolNames: []string{"run", "gated"}}
	started := make(chan struct{})
	handlerErr := make(chan error, 1)
	tools, err := NewToolSet(ModeManual,
		Tool{Definition: assistant.ClientTool{Name: "run"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			close(started)
			<-ctx.Done()
			handlerErr <- ctx.Err()
			return ToolResult{}, ctx.Err()
		}},
		Tool{Definition: assistant.ClientTool{Name: "gated"}, Approval: func(ToolCall) (ApprovalRequirement, bool) {
			return ApprovalRequirement{Key: ApprovalKey{Tool: "gated"}}, true
		}, Handler: func(context.Context, ToolCall) (ToolResult, error) {
			t.Error("stopped tool awaiting approval ran")
			return ToolResult{}, nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, TurnInput{Message: "run both", Tools: tools})
	<-started
	waitForToolStatus(t, events, "call-gated", ToolAwaitingApproval)
	if !engine.StopTools() {
		t.Fatal("StopTools was not accepted during an active round")
	}
	rest := drain(events)

	if err := <-handlerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("running handler context error = %v, want canceled", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("turn only ended through its context (%v); StopTools must end it", err)
	}
	if len(rest) == 0 || rest[len(rest)-1].Kind != EventTurnDone {
		t.Fatalf("stopped turn did not end with EventTurnDone: %+v", rest)
	}
	if backend.calls != 2 || len(backend.responses) != 2 {
		t.Fatalf("backend calls/responses = %d/%d, want 2/2", backend.calls, len(backend.responses))
	}
	for i, id := range []string{"call-run", "call-gated"} {
		response := backend.responses[i]
		if response.ToolCallID != id || response.Title != "Cancelled" || response.Status != assistant.ToolStatusError {
			t.Fatalf("response %d = %+v, want cancelled %s", i, response, id)
		}
		if !hasToolStatus(rest, id, ToolCancelled) {
			t.Fatalf("tool %s never reached cancelled status", id)
		}
	}
	for _, event := range rest {
		for _, block := range event.Transcript.Blocks {
			if block.Markdown != nil && block.Markdown.Content == "done" {
				t.Fatal("follow-up after StopTools was folded into the transcript")
			}
		}
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
		block, ok := stateBlock(event, id)
		if ok && block.Tool != nil && block.Tool.Status == status {
			return received
		}
	}
	t.Fatalf("tool %q never reached status %d", id, status)
	return nil
}

func hasToolStatus(events []Event, id string, status ToolStatus) bool {
	for _, event := range events {
		block, ok := stateBlock(event, id)
		if ok && block.Tool != nil && block.Tool.Status == status {
			return true
		}
	}
	return false
}

// hasDeniedToolBlock reports whether id's terminal block carries the typed
// denial status rather than a plain error.
func hasDeniedToolBlock(t *testing.T, events []Event, id string) bool {
	t.Helper()
	for _, event := range events {
		block, ok := stateBlock(event, id)
		if !ok || block.Tool == nil {
			continue
		}
		if block.Tool.Status == ToolDenied {
			return true
		}
	}
	return false
}

func assertToolResult(t *testing.T, events []Event, id, title string, status ToolStatus) {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		block, ok := stateBlock(events[i], id)
		if !ok {
			continue
		}
		if block.Tool == nil || block.Tool.Status != status || block.Tool.Title != title {
			t.Fatalf("tool %q result = %+v, want status %v titled %q", id, block.Tool, status, title)
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

package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// writeRounds asks for two writes, then one more in a follow-up round.
func writeRounds() [][]assistant.Message {
	return [][]assistant.Message{
		{clientCallMsg("call-a", "write", `{}`), clientCallMsg("call-b", "write", `{}`)},
		{clientCallMsg("call-c", "write", `{}`)},
		{textMsg("answer", "done")},
	}
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
				backend := &scriptBackend{rounds: [][]assistant.Message{{clientCallMsg("call-1", tt.toolName, `{"value":42}`)}, {textMsg("answer", "done")}}}
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

				events := drain(t, New(backend, Config{PermissionsMode: mode}), TurnInput{
					Message: "use a tool",
					Tools:   tools,
				})
				if tt.wantErr != nil {
					if got := events[len(events)-1]; got.Kind != EventError || !errors.Is(got.Err, tt.wantErr) {
						t.Fatalf("last event = %+v", got)
					}
					if len(backend.opts) != 1 {
						t.Fatalf("backend calls = %d, want 1", len(backend.opts))
					}
					return
				}

				batches := backend.responses()
				if len(batches) != 1 || len(batches[0]) != 1 {
					t.Fatalf("tool responses = %v, want one", batches)
				}
				response := batches[0][0]
				if response.ToolCallID != "call-1" || response.Status != tt.wantStatus || response.Title != tt.wantTitle || response.Metadata.Output != tt.wantOutput {
					t.Fatalf("tool response = %+v", response)
				}
				if len(events) == 0 || events[len(events)-1].Kind != EventDone {
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
		grantSession  bool            // granted with the decision, as "Allow for session"
		switchTo      PermissionsMode // switched to just before the decision
		perCallKey    bool            // a distinct approval key per call, so no grant covers a sibling
		wantDecisions int
		wantRuns      int32
		wantCalls     int
		wantMode      PermissionsMode
	}{
		{
			name:          "allow once prompts for sibling and next round",
			decision:      ApprovalAllowOnce,
			wantDecisions: 3,
			wantRuns:      3,
			wantCalls:     3,
			wantMode:      ModeManual,
		},
		{
			name:          "allow session covers sibling and next round",
			decision:      ApprovalAllowOnce,
			grantSession:  true,
			wantDecisions: 1,
			wantRuns:      3,
			wantCalls:     3,
			wantMode:      ModeManual,
		},
		{
			// The modal's full access: the switch re-gates the parked sibling
			// despite its own key, and the next round never parks.
			name:          "full access releases sibling and next round",
			decision:      ApprovalAllowOnce,
			switchTo:      ModeSkipPermissions,
			perCallKey:    true,
			wantDecisions: 1,
			wantRuns:      3,
			wantCalls:     3,
			wantMode:      ModeSkipPermissions,
		},
		{
			// Deny: the denial is answered, then siblings and follow-up calls cancel.
			name:          "deny aborts pending round",
			decision:      ApprovalDeny,
			wantDecisions: 1,
			wantRuns:      0,
			wantCalls:     3,
			wantMode:      ModeManual,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &scriptBackend{rounds: writeRounds()}
			var runs atomic.Int32
			tools, err := NewToolSet(Tool{
				Definition: assistant.ClientTool{Name: "write"},
				Approval: func(call ToolCall) (ApprovalRequirement, bool) {
					resource := "workspace"
					if tt.perCallKey {
						resource = call.ID
					}
					return ApprovalRequirement{
						Key:    ApprovalKey{Tool: "write", Resource: resource},
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
			engine := New(backend, Config{})
			// Interactive surface: stop after the denial is answered on the wire.
			events := turnEvents(engine, ctx, TurnInput{Message: "write", Tools: tools, OnDeny: DenyStop})
			decisions := 0
			var all []Event
			// A switch or a grant releases the parked call itself, as in the modal.
			decide := func(id string) {
				var err error
				switch {
				case tt.switchTo != "":
					err = engine.SetPermissionsMode(tt.switchTo)
				case tt.grantSession:
					err = engine.GrantSessionFor(id)
				default:
					err = engine.Decide(id, tt.decision)
				}
				if err != nil {
					t.Fatalf("decision for %q: %v", id, err)
				}
				decisions++
			}

			// With per-call keys, wait for the sibling to park too, after call-a,
			// so the decision has to release it.
			parked := "call-a"
			if tt.perCallKey {
				parked = "call-b"
			}
			all = append(all, waitForToolStatusEvents(t, events, parked, ToolAwaitingApproval)...)
			decide("call-a")
			if tt.decision == ApprovalAllowOnce && !tt.grantSession && tt.switchTo == "" {
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
			rest := collect(events)
			all = append(all, rest...)

			if decisions != tt.wantDecisions {
				t.Fatalf("decisions = %d, want %d", decisions, tt.wantDecisions)
			}
			if got := runs.Load(); got != tt.wantRuns {
				t.Fatalf("handler runs = %d, want %d", got, tt.wantRuns)
			}
			if len(rest) == 0 || rest[len(rest)-1].Kind != EventDone {
				t.Fatalf("turn did not end cleanly: %v", kinds(rest))
			}
			if len(backend.opts) != tt.wantCalls {
				t.Fatalf("backend calls = %d, want %d", len(backend.opts), tt.wantCalls)
			}
			batches := backend.responses()
			if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 1 {
				t.Fatalf("response batch sizes = %v, want [2 1]", responseBatchSizes(batches))
			}
			if got := engine.PermissionsMode(); got != tt.wantMode {
				t.Fatalf("mode = %q, want %q", got, tt.wantMode)
			}

			if tt.decision == ApprovalDeny {
				// Denial and sibling answer first; the follow-up drains as cancelled.
				if response := batches[0][0]; response.ToolCallID != "call-a" || response.Status != assistant.ToolStatusError || response.Metadata.Output != "local execution was denied by the user" {
					t.Fatalf("denial response = %+v, want the typed error for call-a", response)
				}
				if response := batches[0][1]; response.ToolCallID != "call-b" || response.Status != assistant.ToolStatusError {
					t.Fatalf("sibling response = %+v, want the cancelled result sent on the wire", response)
				}
				if response := batches[1][0]; response.ToolCallID != "call-c" || response.Status != assistant.ToolStatusError || response.Metadata.Output != "tool execution was cancelled by the user" {
					t.Fatalf("follow-up response = %+v, want the cancelled result sent on the wire", response)
				}
				assertToolResult(t, all, "call-a", "Permission denied", ToolDenied)
				assertToolResult(t, all, "call-b", "Cancelled", ToolCancelled)
				// The abort happened before the follow-up round: call-c never decides.
				if hasToolStatus(all, "call-c", ToolAwaitingApproval) {
					t.Fatal("aborted round still processed the model's follow-up round")
				}
			}
		})
	}
}

// TestEngineModeSwitchMidTurnOverridesSessionGrant switches mode while the
// next round's call is being gated, its approval policy still running. Back
// to manual revokes the grant, so that call asks; deny overrides it, so that
// call is denied. Neither may run under the grant once the switch returned.
func TestEngineModeSwitchMidTurnOverridesSessionGrant(t *testing.T) {
	for _, tt := range []struct {
		mode  PermissionsMode
		callC ToolStatus
	}{
		{mode: ModeManual, callC: ToolAwaitingApproval},
		{mode: ModeDeny, callC: ToolDenied},
	} {
		t.Run(string(tt.mode), func(t *testing.T) {
			backend := &scriptBackend{rounds: writeRounds()}
			release := make(chan struct{})
			tools, err := NewToolSet(Tool{
				Definition: assistant.ClientTool{Name: "write"},
				Approval: func(ToolCall) (ApprovalRequirement, bool) {
					return ApprovalRequirement{
						Key:    ApprovalKey{Tool: "write", Resource: "workspace"},
						Prompt: ApprovalPrompt{Title: "Write record?"},
					}, true
				},
				Handler: func(_ context.Context, call ToolCall) (ToolResult, error) {
					if call.ID == "call-b" {
						<-release
					}
					return ToolResult{Title: "Wrote record", Output: "ok"}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			engine := New(backend, Config{})
			events := turnEvents(engine, ctx, TurnInput{Message: "write", Tools: tools})
			waitForToolStatus(t, events, "call-a", ToolAwaitingApproval)
			if !allowForSession(engine, "call-a") {
				t.Fatal("decision for call-a was not applied")
			}
			waitForToolStatus(t, events, "call-b", ToolRunning)
			if engine.GrantSessionFor("call-b") == nil {
				t.Fatal("granted for call-b while it runs")
			}
			// Switch while call-b runs, so call-c, in the next round, is gated
			// under the new mode.
			if err := engine.SetPermissionsMode(tt.mode); err != nil {
				t.Fatal(err)
			}
			close(release)

			waitForToolStatus(t, events, "call-c", tt.callC)
			if tt.callC == ToolAwaitingApproval && engine.Decide("call-c", ApprovalAllowOnce) != nil {
				t.Fatal("decision for call-c was not applied")
			}
			if all := collect(events); len(all) == 0 || all[len(all)-1].Kind != EventDone {
				t.Fatalf("turn did not complete: %v", kinds(all))
			}
			if engine.GrantSessionFor("call-c") == nil {
				t.Fatal("granted for call-c after its turn ended")
			}
		})
	}
}

// TestEngineAbortedRoundUnparksItsCalls cancels a turn while a call waits
// for approval: the call can no longer be granted for.
func TestEngineAbortedRoundUnparksItsCalls(t *testing.T) {
	tools, err := NewToolSet(Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Approval: func(ToolCall) (ApprovalRequirement, bool) {
			return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
		},
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine := New(&scriptBackend{rounds: writeRounds()}, Config{})
	events := turnEvents(engine, ctx, TurnInput{Message: "write", Tools: tools})
	waitForToolStatus(t, events, "call-a", ToolAwaitingApproval)
	cancel()
	collect(events)
	if engine.GrantSessionFor("call-a") == nil {
		t.Fatal("granted for call-a after its turn was cancelled")
	}
}

func TestSetPermissionsModeRejectsInvalidModes(t *testing.T) {
	engine := New(nil, Config{})
	for _, mode := range []PermissionsMode{"", "ask", "allow-all", "gated"} {
		err := engine.SetPermissionsMode(mode)
		if err == nil || !strings.Contains(err.Error(), "manual") || !strings.Contains(err.Error(), "skip-permissions") {
			t.Fatalf("SetPermissionsMode(%q) = %v, want an error listing the valid modes", mode, err)
		}
	}
	if got := engine.PermissionsMode(); got != ModeManual {
		t.Fatalf("mode = %q, want the manual default", got)
	}
}

// The server-injected gate name is reserved; a registered tool with that
// name would be silently intercepted by the engine.
func TestNewToolSetRejectsApprovalRequestName(t *testing.T) {
	set, err := NewToolSet(Tool{
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
	set, err := NewToolSet(Tool{
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
	set, err := NewToolSet(Tool{
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

func TestToolSetPerToolInputStreaming(t *testing.T) {
	ordinary := Tool{
		Definition: assistant.ClientTool{Name: "ordinary"},
		Handler:    func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	}
	withReducer := ordinary
	withReducer.Definition.Name = "preview"
	withReducer.InputReducer = func(context.Context, ToolInputUpdate, any) any { return nil }
	explicit := ordinary
	explicit.Definition.Name = "explicit"
	explicit.Definition.StreamInput = true
	set, err := NewToolSet(ordinary, withReducer, explicit)
	if err != nil {
		t.Fatal(err)
	}
	defs := set.Definitions()
	if defs[0].StreamInput || !defs[1].StreamInput || !defs[2].StreamInput {
		t.Fatalf("streaming definitions = %+v", defs)
	}
	if withReducer.Definition.StreamInput {
		t.Fatal("registration mutated caller's definition")
	}
	defs[1].StreamInput = false
	if !set.Definitions()[1].StreamInput {
		t.Fatal("mutating returned definitions changed registered streaming")
	}
}

func TestToolSetNormalizeResult(t *testing.T) {
	set, err := NewToolSet(Tool{
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

// Approval reports the declared gates, whatever the mode: the tool's policy,
// or the server-injected approval_request gate.
func TestToolSetApprovalReportsDeclaredGates(t *testing.T) {
	declaredGate := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{
			Key:    ApprovalKey{Tool: "write", Resource: "workspace"},
			Prompt: ApprovalPrompt{Title: "Write record?"},
		}, true
	}
	set, err := NewToolSet(
		Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: declaredGate, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }},
	)
	if err != nil {
		t.Fatal(err)
	}
	if requirement, needs := set.Approval(ToolCall{Name: "write"}); !needs || requirement.Prompt.Title != "Write record?" || requirement.Key.Tool != "write" {
		t.Fatalf("Approval(write) = (%+v, %v), want the declared gate", requirement, needs)
	}
	for _, name := range []string{"read", "unknown"} {
		if _, needs := set.Approval(ToolCall{Name: name}); needs {
			t.Fatalf("Approval(%s) reported a gate", name)
		}
	}
	requirement, needs := set.Approval(ToolCall{
		ID:    "gate-1",
		Name:  assistant.ApprovalRequestTool,
		Input: `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-1"}`,
	})
	if !needs || requirement.Key != (ApprovalKey{Tool: assistant.ApprovalRequestTool, Resource: "delete_dashboard"}) || requirement.Prompt.Title != `Allow the assistant to perform the "delete_dashboard" action?` || requirement.Prompt.Detail != "tool: delete_dashboard" {
		t.Fatalf("Approval(approval_request) = (%+v, %v), want the server gate", requirement, needs)
	}
	if gated := requirement.Prompt.Gated; gated == nil || *gated != (ToolCall{ID: "gate-1", Name: "delete_dashboard", Input: `{"dashboard_id":"abc"}`}) {
		t.Fatalf("gated call = %+v, want the server call the request gates", gated)
	}
}

// In a mixed toolset, manual mode gates the gated tool while the ungated
// sibling runs without any decision.
func TestEngineManualModeLeavesUngatedToolsUnblocked(t *testing.T) {
	backend := &scriptBackend{rounds: toolRounds("read", "write")}
	started := make(chan string, 2)
	gate := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
	}
	handler := func(_ context.Context, call ToolCall) (ToolResult, error) {
		started <- call.Name
		return ToolResult{Output: call.Name}, nil
	}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: handler},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: gate, Handler: handler},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(backend, Config{})
	events := turnEvents(engine, ctx, TurnInput{Message: "run both", Tools: tools})

	// The ungated tool runs immediately, before any decision is made.
	if name := receiveName(t, started); name != "read" {
		t.Fatalf("ungated tool did not start before a decision: first started = %q", name)
	}
	waitForToolStatus(t, events, "call-write", ToolAwaitingApproval)
	if !allowForSession(engine, "call-write") {
		t.Fatal("decision for call-write was not queued")
	}
	if name := receiveName(t, started); name != "write" {
		t.Fatalf("second started tool = %q, want write", name)
	}
	_ = collect(events)

	if got := responseIDs(backend.responses()); got != "call-read,call-write" {
		t.Fatalf("responses = %s", got)
	}
}

// Skip-permissions runs gated and ungated tools without a decision and
// approves the server gate on the wire, keeping the response order.
func TestEngineSkipPermissionsSkipsApprovalGates(t *testing.T) {
	rounds := toolRounds("read", "write")
	rounds[0] = append(rounds[0], clientCallMsg("gate-1", assistant.ApprovalRequestTool, `{"tool_name":"delete_dashboard","tool_args":{},"tool_call_id":"gate-1"}`))
	backend := &scriptBackend{rounds: rounds}
	handler := func(_ context.Context, call ToolCall) (ToolResult, error) { return ToolResult{Output: call.Name}, nil }
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: handler},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Approval: writeApproval, Handler: handler},
	)
	if err != nil {
		t.Fatal(err)
	}

	events := drain(t, New(backend, Config{PermissionsMode: ModeSkipPermissions}), TurnInput{Message: "run all", Tools: tools})
	if events[len(events)-1].Kind != EventDone {
		t.Fatalf("turn did not complete: %v", kinds(events))
	}
	for _, id := range []string{"call-read", "call-write", "gate-1"} {
		if hasToolStatus(events, id, ToolAwaitingApproval) || !hasToolStatus(events, id, ToolSuccess) {
			t.Fatalf("tool %q did not run without a decision", id)
		}
	}
	batches := backend.responses()
	if got := responseIDs(batches); got != "call-read,call-write,gate-1" {
		t.Fatalf("responses = %s", got)
	}
	if gate := batches[0][2]; gate.Title != "Approved" || gate.Metadata.Output != "the write was approved" {
		t.Fatalf("gate response = %+v, want an explicit approval", gate)
	}
}

func TestEngineParallelApprovalKeepsCallOrder(t *testing.T) {
	backend := &scriptBackend{rounds: toolRounds("a", "b")}
	started := make(chan string, 2)
	finished := make(chan string, 2)
	gates := map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})}
	approval := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "shared", Resource: "workspace"}}, true
	}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "a"}, Approval: approval, Handler: gatedHandler(started, finished, gates)},
		Tool{Definition: assistant.ClientTool{Name: "b"}, Approval: approval, Handler: gatedHandler(started, finished, gates)},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(backend, Config{})
	events := turnEvents(engine, ctx, TurnInput{Message: "run both", Tools: tools})
	waitForToolStatus(t, events, "call-a", ToolAwaitingApproval)
	allowForSession(engine, "call-a")
	receiveNames(t, started, 2)
	close(gates["b"])
	if name := receiveName(t, finished); name != "b" {
		t.Fatalf("first completed tool = %q, want b", name)
	}
	close(gates["a"])
	_ = collect(events)

	if got := responseIDs(backend.responses()); got != "call-a,call-b" {
		t.Fatalf("responses = %s", got)
	}
}

// StopTools answers every unresolved call in the round as cancelled, persists
// that batch, and ends the turn without folding the backend's follow-up.
func TestEngineStopToolsAnswersRoundAndEndsTurn(t *testing.T) {
	backend := &scriptBackend{rounds: toolRounds("run", "gated")}
	started := make(chan struct{})
	handlerErr := make(chan error, 1)
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "run"}, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
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
	engine := New(backend, Config{})
	events := turnEvents(engine, ctx, TurnInput{Message: "run both", Tools: tools})
	<-started
	waitForToolStatus(t, events, "call-gated", ToolAwaitingApproval)
	if err := engine.StopTools(); err != nil {
		t.Fatalf("StopTools during an active round: %v", err)
	}
	rest := collect(events)

	if err := <-handlerErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("running handler context error = %v, want canceled", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("turn only ended through its context (%v); StopTools must end it", err)
	}
	if len(rest) == 0 || rest[len(rest)-1].Kind != EventDone {
		t.Fatalf("stopped turn did not end with EventDone: %+v", rest)
	}
	batches := backend.responses()
	if len(backend.opts) != 2 || responseIDs(batches) != "call-run,call-gated" {
		t.Fatalf("backend calls/responses = %d/%s, want 2 and both calls", len(backend.opts), responseIDs(batches))
	}
	for i, id := range []string{"call-run", "call-gated"} {
		response := batches[0][i]
		if response.Title != "Cancelled" || response.Status != assistant.ToolStatusError {
			t.Fatalf("response %d = %+v, want cancelled %s", i, response, id)
		}
		if !hasToolStatus(rest, id, ToolCancelled) {
			t.Fatalf("tool %s never reached cancelled status", id)
		}
	}
	for _, event := range rest {
		for _, block := range event.State.Blocks {
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

// allowForSession answers a parked call the way the modal's "Allow for
// session" does: the grant releases the call itself.
func allowForSession(engine *Engine, id string) bool {
	return engine.GrantSessionFor(id) == nil
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

// writeApproval gates writes to the workspace.
func writeApproval(ToolCall) (ApprovalRequirement, bool) {
	return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
}

// responseIDs lists the tool call IDs of every response batch, "|" between
// batches.
func responseIDs(batches [][]assistant.ClientToolResponse) string {
	var ids []string
	for i, batch := range batches {
		if i > 0 {
			ids = append(ids, "|")
		}
		for _, response := range batch {
			ids = append(ids, response.ToolCallID)
		}
	}
	return strings.ReplaceAll(strings.Join(ids, ","), ",|,", "|")
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

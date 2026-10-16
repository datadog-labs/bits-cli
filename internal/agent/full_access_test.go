package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// TestApprovalAllowFullAccessIsValid guards Engine.Decide's gate: a decision
// missing from valid() is silently dropped rather than reaching handleCommand.
func TestApprovalAllowFullAccessIsValid(t *testing.T) {
	if !ApprovalAllowFullAccess.valid() {
		t.Fatal("ApprovalAllowFullAccess.valid() = false, Decide would reject it")
	}
}

// TestApprovalAllowFullAccessReleasesParkedSiblings answers call-a with full
// access: every sibling parked in the round releases despite carrying its own
// approval key, the later round's call-c never parks, and the mode flips at
// decision time rather than at turn end.
func TestApprovalAllowFullAccessReleasesParkedSiblings(t *testing.T) {
	backend := &approvalScopeBackend{t: t}
	var runs atomic.Int32
	tools, err := NewToolSet(ModeManual, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Approval: func(call ToolCall) (ApprovalRequirement, bool) {
			// A distinct key per call: a session grant keyed to call-a would
			// leave call-b and call-c parked.
			return ApprovalRequirement{
				Key:    ApprovalKey{Tool: "write", Resource: call.ID},
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
	events := engine.StartTurn(ctx, TurnInput{Message: "write", Tools: tools})

	waitForToolStatus(t, events, "call-a", ToolAwaitingApproval)
	waitForToolStatus(t, events, "call-b", ToolAwaitingApproval)
	if got := tools.PermissionsMode(); got != ModeManual {
		t.Fatalf("mode = %q before the decision landed, want manual", got)
	}
	if !engine.Decide("call-a", ApprovalAllowFullAccess) {
		t.Fatal("decision for call-a was not queued")
	}

	all := drain(events)
	if got := tools.PermissionsMode(); got != ModeSkipPermissions {
		t.Fatalf("mode = %q, want skip-permissions", got)
	}
	if got := runs.Load(); got != 3 {
		t.Fatalf("handler runs = %d, want 3 (call-a, call-b, and the later call-c all released)", got)
	}
	for _, id := range []string{"call-a", "call-b", "call-c"} {
		if !hasToolStatus(all, id, ToolSuccess) {
			t.Fatalf("tool %q never completed successfully", id)
		}
	}
	if hasToolStatus(all, "call-c", ToolAwaitingApproval) {
		t.Fatal("the later round still gated call-c after full access")
	}
	if len(all) == 0 || all[len(all)-1].Kind != EventTurnDone {
		t.Fatalf("turn did not complete: %v", kinds(all))
	}
}

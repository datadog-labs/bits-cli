package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestDenyStopTransitionsPendingToolsInCallOrder(t *testing.T) {
	for range 20 {
		got := approvalTransitionOrder(t, ApprovalDeny)
		want := []string{"call-a", "call-b", "call-c", "call-d"}
		if !slices.Equal(got, want) {
			t.Fatalf("tool transition order = %v, want %v", got, want)
		}
	}
}

func TestAllowSessionStartsPendingToolsInCallOrder(t *testing.T) {
	for range 20 {
		got := approvalTransitionOrder(t, ApprovalAllowSession)
		want := []string{"call-a", "call-b", "call-c", "call-d"}
		if !slices.Equal(got, want) {
			t.Fatalf("tool transition order = %v, want %v", got, want)
		}
	}
}

func approvalTransitionOrder(t *testing.T, decision ApprovalDecision) []string {
	t.Helper()

	names := []string{"a", "b", "c", "d"}
	ids := []string{"call-a", "call-b", "call-c", "call-d"}
	backend := &batchToolBackend{t: t, toolNames: names}
	release := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})

	approval := func(ToolCall) (ApprovalRequirement, bool) {
		return ApprovalRequirement{Key: ApprovalKey{Tool: "shared", Resource: "workspace"}}, true
	}
	definitions := make([]Tool, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, Tool{
			Definition: assistant.ClientTool{Name: name},
			Approval:   approval,
			Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
				select {
				case <-release:
					return ToolResult{Output: call.Name}, nil
				case <-ctx.Done():
					return ToolResult{}, ctx.Err()
				}
			},
		})
	}
	tools, err := NewToolSet(ModeManual, definitions...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(backend, assistant.SendOptions{})
	events := engine.StartTurn(ctx, TurnInput{Message: "run all", Tools: tools, OnDeny: DenyStop})
	waitForToolStatus(t, events, "call-d", ToolAwaitingApproval)
	if !engine.Decide("call-a", decision) {
		t.Fatal("decision for call-a was not queued")
	}

	wantStatus := ToolError
	var afterDecision []Event
	if decision == ApprovalAllowSession {
		wantStatus = ToolRunning
		for len(toolTransitionOrder(afterDecision, ids, wantStatus)) < len(ids) {
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatal("turn ended before every pending tool started")
				}
				afterDecision = append(afterDecision, event)
			case <-ctx.Done():
				t.Fatal("timed out waiting for pending tools to start")
			}
		}
		close(release)
		released = true
	}
	afterDecision = append(afterDecision, drain(events)...)
	if got := afterDecision[len(afterDecision)-1].Kind; got != EventTurnDone {
		t.Fatalf("last event = %v, want EventTurnDone", got)
	}
	return toolTransitionOrder(afterDecision, ids, wantStatus)
}

func toolTransitionOrder(events []Event, ids []string, status ToolStatus) []string {
	seen := make(map[string]bool, len(ids))
	var order []string
	for _, event := range events {
		if event.Kind != EventTranscript {
			continue
		}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			block, ok := stateBlock(event, id)
			if ok && block.Tool != nil && block.Tool.Status == status {
				seen[id] = true
				order = append(order, id)
			}
		}
	}
	return order
}

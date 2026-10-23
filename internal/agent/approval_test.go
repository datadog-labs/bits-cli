package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

const deleteGateInput = `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-1","approval_message":"Delete it?"}`

var (
	deleteGate   = gateMsg("gate-1", deleteGateInput)
	writeSibling = clientCallMsg("write-1", "write", `{"value":"exact"}`)
)

func gateMsg(id, input string) assistant.Message {
	return clientCallMsg(id, assistant.ApprovalRequestTool, input)
}

// gateRounds streams first, then answers.
func gateRounds(first ...assistant.Message) [][]assistant.Message {
	return [][]assistant.Message{first, {textMsg("answer", "adjusted answer")}}
}

// writeTools registers one write tool, gated when gated is set.
func writeTools(t *testing.T, gated bool, handler ToolHandler) *ToolSet {
	t.Helper()
	tool := Tool{Definition: assistant.ClientTool{Name: "write"}, Handler: handler}
	if gated {
		tool.Approval = writeApproval
	}
	tools, err := NewToolSet(tool)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func written(context.Context, ToolCall) (ToolResult, error) {
	return ToolResult{Output: "written"}, nil
}

// decideGates answers each pending server gate once with decide.
func decideGates(t *testing.T, decide func(id string) bool) func(Event) {
	decided := map[string]bool{}
	return func(event Event) {
		for _, block := range event.State.PendingApprovals() {
			id := block.ToolCallID()
			if block.Tool.Name == assistant.ApprovalRequestTool && !decided[id] {
				decided[id] = true
				if !decide(id) {
					t.Errorf("decision for %s was not applied", id)
				}
			}
		}
	}
}

// summarize renders each response as "id status output".
func summarize(batch []assistant.ClientToolResponse) []string {
	lines := make([]string, len(batch))
	for i, response := range batch {
		lines[i] = fmt.Sprintf("%s %s %s", response.ToolCallID, response.Status, response.Metadata.Output)
	}
	return lines
}

func TestParseServerGateInputRejectsNonContractPayloads(t *testing.T) {
	valid := `{"tool_name":"delete_dashboard","tool_args":{},"tool_call_id":"gate-1"}`
	tests := map[string]string{
		"missing tool name":    `{"tool_args":{},"tool_call_id":"gate-1"}`,
		"missing tool args":    `{"tool_name":"delete_dashboard","tool_call_id":"gate-1"}`,
		"missing call id":      `{"tool_name":"delete_dashboard","tool_args":{}}`,
		"mismatched call id":   `{"tool_name":"delete_dashboard","tool_args":{},"tool_call_id":"other"}`,
		"legacy action":        `{"action":"delete_dashboard"}`,
		"non-object tool args": `{"tool_name":"delete_dashboard","tool_args":[],"tool_call_id":"gate-1"}`,
		"invalid message":      `{"tool_name":"delete_dashboard","tool_args":{},"tool_call_id":"gate-1","approval_message":42}`,
	}
	if _, err := parseServerGateInput(ToolCall{ID: "gate-1", Input: valid}); err != nil {
		t.Fatalf("valid approval request rejected: %v", err)
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseServerGateInput(ToolCall{ID: "gate-1", Input: input}); err == nil {
				t.Fatalf("approval request accepted: %s", input)
			}
		})
	}
}

// Gates that cannot be approved are denied without surfacing an approval, and
// the turn continues or stops as the policy says.
func TestGatesDeniedWithoutAsking(t *testing.T) {
	const modeDenied = "this action was denied by the run's permissions mode"
	for _, tt := range []struct {
		name   string
		round  []assistant.Message
		mode   PermissionsMode
		tools  *ToolSet
		onDeny DenyPolicy
		want   []string
	}{
		{
			name:   "deny mode",
			round:  []assistant.Message{deleteGate, writeSibling},
			mode:   ModeDeny,
			tools:  writeTools(t, true, written),
			onDeny: DenyContinue,
			want:   []string{"gate-1 error " + modeDenied, "write-1 error " + modeDenied},
		},
		{
			name:   "no tool set",
			round:  []assistant.Message{deleteGate},
			onDeny: DenyContinue,
			want:   []string{"gate-1 error the user denied this action"},
		},
		{
			// Even under full access; the stop the denial requests then cancels
			// the sibling dispatched after it.
			name:  "malformed gate",
			round: []assistant.Message{gateMsg("gate-1", `{"tool_name":"delete_dashboard","tool_args":{},"tool_call_id":"other"}`), writeSibling},
			mode:  ModeSkipPermissions,
			tools: writeTools(t, true, written),
			want:  []string{"gate-1 error the server approval request was invalid", "write-1 error tool execution was cancelled by the user"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := &scriptBackend{rounds: gateRounds(tt.round...)}
			engine := New(backend, Config{PermissionsMode: tt.mode})
			events := drain(t, engine, TurnInput{Message: "write", Tools: tt.tools, OnDeny: tt.onDeny}, func(event Event) {
				if len(event.State.PendingApprovals()) > 0 {
					t.Error("a gate surfaced as a pending approval")
				}
			})
			if events[len(events)-1].Kind != EventDone || !hasToolStatus(events, "gate-1", ToolDenied) {
				t.Fatalf("turn = %v, want a completed turn with a typed denial", kinds(events))
			}
			if batches := backend.responses(); len(batches) != 1 || !slices.Equal(summarize(batches[0]), tt.want) {
				t.Fatalf("responses = %v, want %q", batches, tt.want)
			}
		})
	}
}

// An approved server gate is answered on the wire; a session grant covers
// later gates for the same tool but not for another one.
func TestServerGateApprovals(t *testing.T) {
	for _, tt := range []struct {
		name      string
		round     []assistant.Message
		session   bool
		decisions int
	}{
		{name: "allow once", round: []assistant.Message{deleteGate}, decisions: 1},
		{
			name:      "session grant is tool scoped",
			round:     []assistant.Message{deleteGate, gateMsg("gate-2", `{"tool_name":"update_dashboard","tool_args":{},"tool_call_id":"gate-2"}`)},
			session:   true,
			decisions: 2,
		},
		{
			name:      "session grant covers the same tool",
			round:     []assistant.Message{deleteGate, gateMsg("gate-2", `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"def"},"tool_call_id":"gate-2"}`)},
			session:   true,
			decisions: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := &scriptBackend{rounds: gateRounds(tt.round...)}
			engine := New(backend, Config{})
			decisions := 0
			events := drain(t, engine, TurnInput{Message: "write", Tools: writeTools(t, true, written)},
				func(event Event) {
					for _, block := range event.State.PendingApprovals() {
						if block.ToolCallID() == "gate-1" && (block.Tool.Approval == nil || block.Tool.Approval.Title != "Delete it?" || block.Tool.Approval.Detail != "tool: delete_dashboard") {
							t.Errorf("server-gate prompt = %+v, want the action-specific prompt", block.Tool.Approval)
						}
					}
				},
				decideGates(t, func(id string) bool {
					if decisions == tt.decisions {
						return true // covered by the session grant
					}
					decisions++
					if tt.session {
						return allowForSession(engine, id)
					}
					return engine.Decide(id, ApprovalAllowOnce) == nil
				}))
			if events[len(events)-1].Kind != EventDone || decisions != tt.decisions {
				t.Fatalf("turn = %v, decisions = %d, want a completed turn after %d", kinds(events), decisions, tt.decisions)
			}
			batches := backend.responses()
			if len(batches) != 1 || len(batches[0]) != len(tt.round) {
				t.Fatalf("responses = %v", batches)
			}
			for _, response := range batches[0] {
				if response.Status != assistant.ToolStatusSuccess {
					t.Fatalf("response = %+v, want success", response)
				}
			}
		})
	}
}

// A denied server gate is answered on the wire next to its siblings. Under
// DenyStop (the zero value) the turn ends without the model's follow-up and
// pending siblings are cancelled; under DenyContinue the model adjusts.
func TestServerGateDenials(t *testing.T) {
	const denied = "gate-1 error the user denied this action"
	for _, tt := range []struct {
		name         string
		round        []assistant.Message
		onDeny       DenyPolicy
		gatedSibling bool
		siblingFails bool
		want         []string
	}{
		{name: "continue", round: []assistant.Message{deleteGate, writeSibling}, onDeny: DenyContinue, want: []string{denied, "write-1 success written"}},
		{name: "stop", round: []assistant.Message{deleteGate, writeSibling}, want: []string{denied, "write-1 success written"}},
		{
			name:         "stop cancels a pending sibling",
			round:        []assistant.Message{writeSibling, deleteGate},
			gatedSibling: true,
			want:         []string{"write-1 error tool execution was cancelled by the user", denied},
		},
		{name: "stop records a failing sibling", round: []assistant.Message{deleteGate, writeSibling}, siblingFails: true, want: []string{denied, "write-1 error handler exploded"}},
		{name: "continue records a failing sibling", round: []assistant.Message{deleteGate, writeSibling}, onDeny: DenyContinue, siblingFails: true, want: []string{denied, "write-1 error handler exploded"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			handler := written
			if tt.siblingFails {
				// The sibling fails after the denial, so the round is already stopping.
				release := make(chan struct{})
				time.AfterFunc(50*time.Millisecond, func() { close(release) })
				handler = func(context.Context, ToolCall) (ToolResult, error) {
					<-release
					return ToolResult{}, errors.New("handler exploded")
				}
			}
			backend := &scriptBackend{rounds: gateRounds(tt.round...)}
			engine := New(backend, Config{})
			events := drain(t, engine, TurnInput{Message: "write", Tools: writeTools(t, tt.gatedSibling, handler), OnDeny: tt.onDeny},
				decideGates(t, func(id string) bool { return engine.Decide(id, ApprovalDeny) == nil }))
			if events[len(events)-1].Kind != EventDone || !hasToolStatus(events, "gate-1", ToolDenied) {
				t.Fatalf("turn = %v, want a completed turn with a typed denial", kinds(events))
			}
			if batches := backend.responses(); len(batches) != 1 || !slices.Equal(summarize(batches[0]), tt.want) {
				t.Fatalf("responses = %v, want %q", batches, tt.want)
			}
			answered := slices.ContainsFunc(engine.State().Blocks, func(block Block) bool {
				return block.Markdown != nil && block.Markdown.Content == "adjusted answer"
			})
			if answered != (tt.onDeny == DenyContinue) {
				t.Fatalf("follow-up folded = %t under policy %v", answered, tt.onDeny)
			}
		})
	}
}

// A denial excuses handler failures only in its own round: a later round's
// failure still fails the turn.
func TestDenialDoesNotExcuseLaterRoundFailures(t *testing.T) {
	failure := errors.New("handler exploded")
	backend := &scriptBackend{rounds: [][]assistant.Message{{deleteGate}, {writeSibling}, {textMsg("answer", "unreachable")}}}
	engine := New(backend, Config{})
	tools := writeTools(t, false, func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, failure })
	events := drain(t, engine, TurnInput{Message: "write", Tools: tools, OnDeny: DenyContinue},
		decideGates(t, func(id string) bool { return engine.Decide(id, ApprovalDeny) == nil }))
	if last := events[len(events)-1]; last.Kind != EventError || !errors.Is(last.Err, failure) {
		t.Fatalf("last event = %+v, want the round-2 handler failure", last)
	}
	if len(backend.opts) != 2 {
		t.Fatalf("backend calls = %d, want 2: the failure is not answered", len(backend.opts))
	}
}

func TestDenyStopDrainsFollowUpClientToolCalls(t *testing.T) {
	backend := &scriptBackend{rounds: [][]assistant.Message{{deleteGate}, {clientCallMsg("follow-1", "write", `{"value":"discarded"}`)}, {textMsg("answer", "adjusted answer")}}}
	engine := New(backend, Config{})
	events := drain(t, engine, TurnInput{Message: "write", Tools: writeTools(t, true, written)},
		decideGates(t, func(id string) bool { return engine.Decide(id, ApprovalDeny) == nil }))
	if last := events[len(events)-1]; last.Kind != EventDone || last.Round != 3 {
		t.Fatalf("last event = %+v, want the turn done at round 3", last)
	}
	if got := responseIDs(backend.responses()); got != "gate-1|follow-1" {
		t.Fatalf("responses = %s", got)
	}
	if drained := backend.responses()[1][0]; drained.Status != assistant.ToolStatusError || drained.Metadata.Output != "tool execution was cancelled by the user" {
		t.Fatalf("drained response = %+v", drained)
	}
	for _, block := range engine.State().Blocks {
		if block.ToolCallID() == "follow-1" || block.Markdown != nil && block.Markdown.Content == "adjusted answer" {
			t.Fatalf("stopped follow-up was folded into the transcript: %+v", block)
		}
	}
}

func TestDenyStopBackendFailureRetainsBackendProvenance(t *testing.T) {
	backendErr := errors.New("backend failed while recording denial")
	backend := &scriptBackend{rounds: [][]assistant.Message{{deleteGate}}, err: backendErr, failOn: 2}
	engine := New(backend, Config{})
	events := drain(t, engine, TurnInput{Message: "write", Tools: writeTools(t, true, written)},
		decideGates(t, func(id string) bool { return engine.Decide(id, ApprovalDeny) == nil }))
	if last := events[len(events)-1]; last.Kind != EventError || !last.BackendFailure || !errors.Is(last.Err, backendErr) {
		t.Fatalf("last event = %+v, want the backend failure", last)
	}
}

// Answering the first of several parked calls transitions its siblings in
// call order: all cancelled after a denial, all started after a session grant.
func TestApprovalTransitionsParkedCallsInOrder(t *testing.T) {
	ids := []string{"call-a", "call-b", "call-c", "call-d"}
	for _, allowSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowSession=%t", allowSession), func(t *testing.T) {
			for range 20 {
				if got := approvalTransitionOrder(t, allowSession, ids); !slices.Equal(got, ids) {
					t.Fatalf("tool transition order = %v, want %v", got, ids)
				}
			}
		})
	}
}

func approvalTransitionOrder(t *testing.T, allowSession bool, ids []string) []string {
	t.Helper()
	release := make(chan struct{})
	defer func() {
		if !allowSession {
			close(release)
		}
	}()
	var definitions []Tool
	for _, name := range []string{"a", "b", "c", "d"} {
		definitions = append(definitions, Tool{
			Definition: assistant.ClientTool{Name: name},
			Approval: func(ToolCall) (ApprovalRequirement, bool) {
				return ApprovalRequirement{Key: ApprovalKey{Tool: "shared", Resource: "workspace"}}, true
			},
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
	tools, err := NewToolSet(definitions...)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	engine := New(&scriptBackend{rounds: toolRounds("a", "b", "c", "d")}, Config{})
	events := turnEvents(engine, ctx, TurnInput{Message: "run all", Tools: tools, OnDeny: DenyStop})
	waitForToolStatus(t, events, "call-d", ToolAwaitingApproval)
	wantStatuses := []ToolStatus{ToolDenied, ToolCancelled}
	var afterDecision []Event
	if allowSession {
		if !allowForSession(engine, "call-a") {
			t.Fatal("decision for call-a was not queued")
		}
		wantStatuses = []ToolStatus{ToolRunning}
		for len(toolTransitionOrder(afterDecision, ids, wantStatuses...)) < len(ids) {
			event, ok := <-events
			if !ok {
				t.Fatal("turn ended before every pending tool started")
			}
			afterDecision = append(afterDecision, event)
		}
		close(release)
	} else if err := engine.Decide("call-a", ApprovalDeny); err != nil {
		t.Fatalf("decision for call-a: %v", err)
	}
	afterDecision = append(afterDecision, collect(events)...)
	if got := afterDecision[len(afterDecision)-1].Kind; got != EventDone {
		t.Fatalf("last event = %v, want EventDone", got)
	}
	return toolTransitionOrder(afterDecision, ids, wantStatuses...)
}

func toolTransitionOrder(events []Event, ids []string, statuses ...ToolStatus) []string {
	seen := make(map[string]bool, len(ids))
	var order []string
	for _, event := range events {
		for _, id := range ids {
			block, ok := stateBlock(event, id)
			if !seen[id] && ok && block.Tool != nil && slices.Contains(statuses, block.Tool.Status) {
				seen[id] = true
				order = append(order, id)
			}
		}
	}
	return order
}

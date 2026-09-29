package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// gateBackend scripts a server-injected approval gate and optional siblings;
// later sends record response batches and answer, or fail with failFollowUp.
type gateBackend struct {
	failFollowUp      error
	includeSibling    bool
	includeSecondGate bool
	siblingFirst      bool
	followUpToolCall  bool
	gateInput         string
	secondGateInput   string
	responses         [][]assistant.ClientToolResponse
	calls             int
}

func clientToolCall(convID, msgID, callID, name, input string) assistant.AssistantResponse {
	content := assistant.ToolCallContent(callID, name, input)
	content.Type = assistant.ContentClientToolCall
	var response assistant.AssistantResponse
	response.Data.Attributes.ConversationID = convID
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage(msgID, content)
	return response
}

func (b *gateBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	if b.calls == 1 {
		emitSibling := func() error {
			return emit(clientToolCall("conversation-1", "write-message", "write-1", "write", `{"value":"exact"}`))
		}
		if b.includeSibling && b.siblingFirst {
			if err := emitSibling(); err != nil {
				return "conversation-1", err
			}
		}
		gateInput := b.gateInput
		if gateInput == "" {
			gateInput = `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-1","approval_message":"Delete it?"}`
		}
		if err := emit(clientToolCall("conversation-1", "gate-message", "gate-1", assistant.ApprovalRequestTool, gateInput)); err != nil {
			return "conversation-1", err
		}
		if b.includeSecondGate {
			secondGateInput := b.secondGateInput
			if secondGateInput == "" {
				secondGateInput = `{"tool_name":"update_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"gate-2","approval_message":"Update it?"}`
			}
			if err := emit(clientToolCall("conversation-1", "second-gate-message", "gate-2", assistant.ApprovalRequestTool, secondGateInput)); err != nil {
				return "conversation-1", err
			}
		}
		if b.includeSibling && !b.siblingFirst {
			if err := emitSibling(); err != nil {
				return "conversation-1", err
			}
		}
		return "conversation-1", nil
	}
	responses, ok := message.([]assistant.ClientToolResponse)
	wantResponses := 1
	if b.calls == 2 && b.includeSibling {
		wantResponses++
	}
	if b.calls == 2 && b.includeSecondGate {
		wantResponses++
	}
	if !ok || len(responses) != wantResponses {
		return "conversation-1", errors.New("follow-up carried the wrong client tool response count")
	}
	b.responses = append(b.responses, responses)
	if b.failFollowUp != nil {
		return "conversation-1", b.failFollowUp
	}
	if b.calls == 2 && b.followUpToolCall {
		return "conversation-1", emit(clientToolCall("conversation-1", "follow-message", "follow-1", "write", `{"value":"discarded"}`))
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.ConversationID = "conversation-1"
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("adjusted answer"))
	return "conversation-1", emit(response)
}

// gatedToolSet builds a set with one gated local tool, the same shape chat uses.
func gatedToolSet(t *testing.T, mode PermissionsMode) *ToolSet {
	t.Helper()
	tools, err := NewToolSet(mode, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Approval: func(ToolCall) (ApprovalRequirement, bool) {
			return ApprovalRequirement{Key: ApprovalKey{Tool: "write", Resource: "workspace"}}, true
		},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "written"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func decideServerGate(engine *Engine, decision ApprovalDecision, observe func(Event) error) func(Event) error {
	decisionIssued := false
	return func(event Event) error {
		if observe != nil {
			if err := observe(event); err != nil {
				return err
			}
		}
		if event.Kind != EventTranscript {
			return nil
		}
		if decisionIssued {
			return nil
		}
		for _, block := range event.Transcript.PendingApprovals() {
			if block.Tool == nil || block.Tool.Name != assistant.ApprovalRequestTool {
				continue
			}
			id := block.ToolCallID()
			if !engine.Decide(id, decision) {
				return errors.New("server-gate approval decision was not queued")
			}
			decisionIssued = true
			break
		}
		return nil
	}
}

func TestEngineApprovalRequestGateSkipPermissionsApproves(t *testing.T) {
	backend := &gateBackend{}
	result, err := New(backend, assistant.SendOptions{}).RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeSkipPermissions),
	}, nil)
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if result.Denied {
		t.Fatal("skip-permissions reported a denial")
	}
	if len(backend.responses) != 1 {
		t.Fatalf("response batches = %d, want 1", len(backend.responses))
	}
	response := backend.responses[0][0]
	if response.ToolCallID != "gate-1" || response.Status != assistant.ToolStatusSuccess {
		t.Fatalf("gate response = %+v, want success for gate-1", response)
	}
	if response.Title != "Approved" || response.Metadata.Output != "the write was approved" {
		t.Fatalf("gate response = %+v, want an explicit approval", response)
	}
}

func TestEngineApprovalRequestGateNilToolsDenies(t *testing.T) {
	backend := &gateBackend{}
	result, err := New(backend, assistant.SendOptions{}).RunTurn(context.Background(), TurnInput{
		Message: "write something",
		OnDeny:  DenyContinue,
	}, nil)
	if err != nil || result.Outcome != TurnOutcomeCompleted || !result.Denied {
		t.Fatalf("result/error = %+v, %v; want a typed denial", result, err)
	}
	if len(backend.responses) != 1 {
		t.Fatalf("response batches = %d, want 1", len(backend.responses))
	}
	response := backend.responses[0][0]
	if response.ToolCallID != "gate-1" || response.Status != assistant.ToolStatusError || response.Metadata.Output != "the user denied this action" {
		t.Fatalf("gate response = %+v, want a server-gate denial", response)
	}
}

func TestEngineMalformedApprovalRequestFailsClosed(t *testing.T) {
	backend := &gateBackend{gateInput: `{"tool_name":"delete_dashboard","tool_args":{},"tool_call_id":"other"}`}
	result, err := New(backend, assistant.SendOptions{}).RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeSkipPermissions),
	}, nil)
	if err != nil || result.Outcome != TurnOutcomeCompleted || !result.Denied {
		t.Fatalf("result/error = %+v, %v; want a typed denial", result, err)
	}
	if len(backend.responses) != 1 {
		t.Fatalf("response batches = %d, want 1", len(backend.responses))
	}
	response := backend.responses[0][0]
	if response.Status != assistant.ToolStatusError || response.Title != "Invalid approval request" || response.Metadata.Output != "the server approval request was invalid" {
		t.Fatalf("gate response = %+v, want an invalid-request error", response)
	}
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

func TestEngineApprovalRequestGateManualApprovesInteractively(t *testing.T) {
	backend := &gateBackend{}
	engine := New(backend, assistant.SendOptions{})
	sawPending := false
	consume := decideServerGate(engine, ApprovalAllowOnce, func(event Event) error {
		if event.Kind != EventTranscript {
			return nil
		}
		for _, block := range event.Transcript.PendingApprovals() {
			if block.Tool == nil || block.Tool.Name != assistant.ApprovalRequestTool {
				continue
			}
			sawPending = true
			if block.Tool.Approval == nil || block.Tool.Approval.Title != "Delete it?" || block.Tool.Approval.Detail != "tool: delete_dashboard" {
				t.Fatalf("server-gate prompt = %+v, want the action-specific approval prompt", block.Tool.Approval)
			}
		}
		return nil
	})
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
	}, consume)
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if !sawPending {
		t.Fatal("manual-mode server gate never surfaced as a pending approval")
	}
	if result.Denied {
		t.Fatal("approved server gate was reported as denied")
	}
	if len(backend.responses) != 1 {
		t.Fatalf("response batches = %d, want 1", len(backend.responses))
	}
	response := backend.responses[0][0]
	if response.ToolCallID != "gate-1" || response.Status != assistant.ToolStatusSuccess {
		t.Fatalf("gate response = %+v, want success for gate-1", response)
	}
}

func TestEngineApprovalRequestGateAllowSessionIsToolScoped(t *testing.T) {
	backend := &gateBackend{includeSecondGate: true}
	engine := New(backend, assistant.SendOptions{})
	decisions := 0
	decided := make(map[string]struct{})
	consume := func(event Event) error {
		if event.Kind != EventTranscript {
			return nil
		}
		for _, block := range event.Transcript.PendingApprovals() {
			if block.Tool == nil || block.Tool.Name != assistant.ApprovalRequestTool {
				continue
			}
			if _, done := decided[block.ToolCallID()]; done {
				continue
			}
			decisions++
			if !engine.Decide(block.ToolCallID(), ApprovalAllowSession) {
				return errors.New("server-gate session approval was not queued")
			}
			decided[block.ToolCallID()] = struct{}{}
			return nil
		}
		return nil
	}
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
	}, consume)
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if decisions != 2 {
		t.Fatalf("server-gate decisions = %d, want one decision per action", decisions)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 2 {
		t.Fatalf("response batches = %+v, want both server gates", backend.responses)
	}
	for _, response := range backend.responses[0] {
		if response.Status != assistant.ToolStatusSuccess {
			t.Fatalf("server-gate response = %+v, want success", response)
		}
	}
}

func TestEngineApprovalRequestGateAllowSessionCoversSameTool(t *testing.T) {
	backend := &gateBackend{
		includeSecondGate: true,
		secondGateInput:   `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"def"},"tool_call_id":"gate-2","approval_message":"Delete another one?"}`,
	}
	engine := New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := engine.RunTurn(ctx, TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
	}, decideServerGate(engine, ApprovalAllowSession, nil))
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 2 {
		t.Fatalf("response batches = %+v, want both same-tool gates", backend.responses)
	}
	for _, response := range backend.responses[0] {
		if response.Status != assistant.ToolStatusSuccess {
			t.Fatalf("server-gate response = %+v, want success", response)
		}
	}
}

func TestEngineApprovalRequestGateManualDeniesAndContinues(t *testing.T) {
	backend := &gateBackend{}
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
		OnDeny:  DenyContinue,
	}, decideServerGate(engine, ApprovalDeny, nil))
	// The denial is typed and the turn continues with an adjusted answer.
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if !result.Denied {
		t.Fatal("manual-mode denial was not reported as a typed outcome")
	}
	if len(backend.responses) != 1 {
		t.Fatalf("response batches = %d, want 1", len(backend.responses))
	}
	response := backend.responses[0][0]
	if response.ToolCallID != "gate-1" || response.Status != assistant.ToolStatusError {
		t.Fatalf("gate response = %+v, want an error response for gate-1", response)
	}
	if response.Metadata.Output != "the user denied this action" {
		t.Fatalf("gate response = %+v, want the denial text the model adjusts to", response)
	}
	// The denial is typed on the tool block.
	denied := false
	for _, block := range result.Blocks {
		if block.ToolCallID() == "gate-1" && block.Tool != nil && block.Tool.Denied && block.Tool.Status == ToolError {
			denied = true
		}
	}
	if !denied {
		t.Fatal("denial was not folded into the transcript as a typed tool block")
	}
}

func TestEngineDeniedGatePreservesSiblingResultOrder(t *testing.T) {
	backend := &gateBackend{includeSibling: true}
	tools, err := NewToolSet(ModeManual, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "written"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   tools,
		OnDeny:  DenyContinue,
	}, decideServerGate(engine, ApprovalDeny, nil))
	if err != nil || result.Outcome != TurnOutcomeCompleted || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 2 {
		t.Fatalf("response batches = %+v, want one ordered pair", backend.responses)
	}
	denial, sibling := backend.responses[0][0], backend.responses[0][1]
	if denial.ToolCallID != "gate-1" || denial.Status != assistant.ToolStatusError {
		t.Fatalf("first response = %+v, want the gate denial", denial)
	}
	if sibling.ToolCallID != "write-1" || sibling.Status != assistant.ToolStatusSuccess || sibling.Metadata.Output != "written" {
		t.Fatalf("second response = %+v, want the sibling result", sibling)
	}
}

func TestDenyPolicyZeroValueStopsAfterWireAnswer(t *testing.T) {
	backend := &gateBackend{includeSibling: true}
	tools, err := NewToolSet(ModeManual, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "written"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   tools,
	}, decideServerGate(engine, ApprovalDeny, nil))
	if err != nil || result.Outcome != TurnOutcomeCompleted || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 2 {
		t.Fatalf("wire responses = %+v, want the denial and sibling result before stopping", backend.responses)
	}
	if denial, sibling := backend.responses[0][0], backend.responses[0][1]; denial.Status != assistant.ToolStatusError || sibling.Status != assistant.ToolStatusSuccess || sibling.Metadata.Output != "written" {
		t.Fatalf("wire responses = %+v, want ordered denial then sibling success", backend.responses[0])
	}
	for _, block := range result.Blocks {
		if block.Markdown != nil && block.Markdown.Content == "adjusted answer" {
			t.Fatal("zero-value stop policy folded the model follow-up")
		}
	}
}

func TestDenyStopCancelsPendingSiblingBeforeServerGate(t *testing.T) {
	backend := &gateBackend{includeSibling: true, siblingFirst: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(ctx, TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
	}, decideServerGate(engine, ApprovalDeny, nil))
	if err != nil || result.Outcome != TurnOutcomeCompleted || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 2 {
		t.Fatalf("wire responses = %+v, want the pending sibling and gate denial", backend.responses)
	}
	sibling, denial := backend.responses[0][0], backend.responses[0][1]
	if sibling.ToolCallID != "write-1" || sibling.Status != assistant.ToolStatusError || sibling.Metadata.Output != "tool execution was cancelled by the user" {
		t.Fatalf("first response = %+v, want cancelled pending sibling", sibling)
	}
	if denial.ToolCallID != "gate-1" || denial.Status != assistant.ToolStatusError || denial.Metadata.Output != "the user denied this action" {
		t.Fatalf("second response = %+v, want server-gate denial", denial)
	}
}

func TestDenyStopDrainsFollowUpClientToolCalls(t *testing.T) {
	backend := &gateBackend{followUpToolCall: true}
	terminalRound := 0
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
	}, decideServerGate(engine, ApprovalDeny, func(event Event) error {
		if event.Kind == EventTurnDone {
			terminalRound = event.Round
		}
		return nil
	}))
	if err != nil || result.Outcome != TurnOutcomeCompleted || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if backend.calls != 3 || len(backend.responses) != 2 {
		t.Fatalf("backend calls/batches = %d/%d, want 3/2", backend.calls, len(backend.responses))
	}
	if terminalRound != 3 {
		t.Fatalf("terminal round = %d, want 3 backend sends", terminalRound)
	}
	denial, drained := backend.responses[0][0], backend.responses[1][0]
	if denial.ToolCallID != "gate-1" || denial.Status != assistant.ToolStatusError {
		t.Fatalf("denial response = %+v", denial)
	}
	if drained.ToolCallID != "follow-1" || drained.Status != assistant.ToolStatusError || drained.Metadata.Output != "tool execution was cancelled by the user" {
		t.Fatalf("drained response = %+v", drained)
	}
	for _, block := range result.Blocks {
		if block.ToolCallID() == "follow-1" || block.Markdown != nil && block.Markdown.Content == "adjusted answer" {
			t.Fatalf("stopped follow-up was folded into the transcript: %+v", block)
		}
	}
}

func TestDenyStopBackendFailureRetainsBackendProvenance(t *testing.T) {
	backendErr := errors.New("backend failed while recording denial")
	backend := &gateBackend{failFollowUp: backendErr}
	var failure Event
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
	}, decideServerGate(engine, ApprovalDeny, func(event Event) error {
		if event.Kind == EventError {
			failure = event
		}
		return nil
	}))

	if !errors.Is(err, backendErr) || result.Outcome != TurnOutcomeFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if !failure.BackendFailure || !errors.Is(failure.Err, backendErr) {
		t.Fatalf("failure event = %+v, want backend provenance", failure)
	}
}

func TestDeniedGateRecordsFailingSiblingOnTheWire(t *testing.T) {
	backend := &gateBackend{includeSibling: true}
	release := make(chan struct{})
	tools, err := NewToolSet(ModeManual, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			<-release
			return ToolResult{}, errors.New("handler exploded")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(ctx, TurnInput{
		Message: "write something",
		Tools:   tools,
	}, decideServerGate(engine, ApprovalDeny, nil))
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 2 {
		t.Fatalf("wire responses = %+v, want denial and failed sibling", backend.responses)
	}

	denial, sibling := backend.responses[0][0], backend.responses[0][1]
	if denial.ToolCallID != "gate-1" || denial.Status != assistant.ToolStatusError {
		t.Fatalf("denial response = %+v", denial)
	}
	if sibling.ToolCallID != "write-1" || sibling.Status != assistant.ToolStatusError || sibling.Metadata.Output != "handler exploded" {
		t.Fatalf("sibling response = %+v, want the recorded failure", sibling)
	}
}

func TestRunTurnDeniedGateThenBackendFailureFails(t *testing.T) {
	backendErr := errors.New("backend failed after denial")
	backend := &gateBackend{failFollowUp: backendErr}
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeManual),
		OnDeny:  DenyContinue,
	}, decideServerGate(engine, ApprovalDeny, nil))
	// Runtime failure wins; the denial evidence remains.
	if !errors.Is(err, backendErr) || result.Outcome != TurnOutcomeFailed {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if !result.Denied {
		t.Fatal("denial evidence was lost on the failing turn")
	}
}

// Under DenyContinue a sibling handler failure must not drop the round's
// wire batch: the failure is recorded and the model gets to adjust.
func TestDenyContinueRecordsFailingSiblingOnTheWire(t *testing.T) {
	backend := &gateBackend{includeSibling: true}
	release := make(chan struct{})
	tools, err := NewToolSet(ModeManual, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			<-release
			return ToolResult{}, errors.New("handler exploded")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	engine := New(backend, assistant.SendOptions{})
	result, err := engine.RunTurn(ctx, TurnInput{
		Message: "write something",
		Tools:   tools,
		OnDeny:  DenyContinue,
	}, decideServerGate(engine, ApprovalDeny, nil))
	if err != nil || result.Outcome != TurnOutcomeCompleted || !result.Denied {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if len(backend.responses) != 1 || len(backend.responses[0]) != 2 {
		t.Fatalf("wire responses = %+v, want denial and failed sibling", backend.responses)
	}
	denial, sibling := backend.responses[0][0], backend.responses[0][1]
	if denial.ToolCallID != "gate-1" || denial.Status != assistant.ToolStatusError {
		t.Fatalf("denial response = %+v", denial)
	}
	if sibling.ToolCallID != "write-1" || sibling.Status != assistant.ToolStatusError || sibling.Metadata.Output != "handler exploded" {
		t.Fatalf("sibling response = %+v, want the recorded failure", sibling)
	}
}

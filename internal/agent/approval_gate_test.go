package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// gateBackend scripts a server-injected approval_request gate: round 1 emits
// the gate call (and optionally a registered client tool call); later sends
// record response batches and answer.
type gateBackend struct {
	includeSibling   bool
	siblingFirst     bool
	followUpToolCall bool
	responses        [][]assistant.ClientToolResponse
	calls            int
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
		if err := emit(clientToolCall("conversation-1", "gate-message", "gate-1", assistant.ApprovalRequestTool, `{"action":"delete_dashboard"}`)); err != nil {
			return "conversation-1", err
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
		wantResponses = 2
	}
	if !ok || len(responses) != wantResponses {
		return "conversation-1", errors.New("follow-up carried the wrong client tool response count")
	}
	b.responses = append(b.responses, responses)
	if b.calls == 2 && b.followUpToolCall {
		return "conversation-1", emit(clientToolCall("conversation-1", "follow-message", "follow-1", "write", `{"value":"discarded"}`))
	}
	var response assistant.AssistantResponse
	response.Data.Attributes.ConversationID = "conversation-1"
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("adjusted answer"))
	return "conversation-1", emit(response)
}

// gatedToolSet builds a set with one gated local tool, the same shape chat uses.
func gatedToolSet(t *testing.T, mode ApprovalMode) *ToolSet {
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

func TestEngineApprovalRequestGateAllowAllApproves(t *testing.T) {
	backend := &gateBackend{}
	result, err := New(backend, assistant.SendOptions{}).RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeAllowAll),
	}, nil)
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
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

func TestDeniedServerGateStopsAfterWireAnswer(t *testing.T) {
	backend := &gateBackend{includeSibling: true}
	tools, err := NewToolSet(ModeGated, Tool{
		Definition: assistant.ClientTool{Name: "write"},
		Handler: func(context.Context, ToolCall) (ToolResult, error) {
			return ToolResult{Output: "written"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(backend, assistant.SendOptions{}).RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   tools,
	}, nil)
	if err != nil || result.Outcome != TurnOutcomeCompleted {
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

func TestDeniedServerGateCancelsEarlierPendingSibling(t *testing.T) {
	backend := &gateBackend{includeSibling: true, siblingFirst: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := New(backend, assistant.SendOptions{}).RunTurn(ctx, TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeGated),
	}, nil)
	if err != nil || result.Outcome != TurnOutcomeCompleted {
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

func TestDeniedGateDrainsFollowUpClientToolCalls(t *testing.T) {
	backend := &gateBackend{followUpToolCall: true}
	result, err := New(backend, assistant.SendOptions{}).RunTurn(context.Background(), TurnInput{
		Message: "write something",
		Tools:   gatedToolSet(t, ModeGated),
	}, nil)
	if err != nil || result.Outcome != TurnOutcomeCompleted {
		t.Fatalf("result/error = %+v, %v", result, err)
	}
	if backend.calls != 3 || len(backend.responses) != 2 {
		t.Fatalf("backend calls/batches = %d/%d, want 3/2", backend.calls, len(backend.responses))
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

// A sibling that fails after a denied gate must be answered on the wire as a
// failed result instead of aborting the stopped round.
func TestDeniedGateRecordsFailingSiblingOnTheWire(t *testing.T) {
	backend := &gateBackend{includeSibling: true}
	release := make(chan struct{})
	tools, err := NewToolSet(ModeGated, Tool{
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
	result, err := New(backend, assistant.SendOptions{}).RunTurn(ctx, TurnInput{
		Message: "write something",
		Tools:   tools,
	}, nil)
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

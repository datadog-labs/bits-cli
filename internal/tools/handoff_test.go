package tools

import (
	"context"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

const handoffConversation = "00000000-0000-4000-8000-000000000001"

type handoffBackend struct {
	history   []assistant.Message
	responses []assistant.ClientToolResponse
	tools     []assistant.ClientTool
	sends     int
}

func (b *handoffBackend) ConversationHistory(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	response := &assistant.ConversationHistoryResponse{}
	response.Data.ID = "history"
	response.Data.Type = "conversation-history-response"
	response.Data.Attributes.Messages = b.history
	return response, nil
}

func (b *handoffBackend) Send(_ context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.sends++
	b.tools = opts.ClientTools
	b.responses, _ = message.([]assistant.ClientToolResponse)
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("local", assistant.TextContent("Reading the repository."))
	return handoffConversation, emit(response)
}

func webHandoffHistory(input string) []assistant.Message {
	user := assistant.Message{MessageID: "user", Role: "user", Content: assistant.TextContent("fix the flaky test")}
	call := assistant.ToolCallContent("handoff-1", spec.Handoff, input)
	call.Type = assistant.ContentClientToolCall
	return []assistant.Message{user, assistant.AssistantMessage("call", call)}
}

func loadHandoff(t *testing.T, backend *handoffBackend) (*agent.Engine, *agent.ToolSet) {
	t.Helper()
	set, err := agent.NewToolSet(NewHandoffTool())
	if err != nil {
		t.Fatal(err)
	}
	engine := agent.New(backend, agent.Config{})
	conversation, err := engine.LoadConversation(t.Context(), handoffConversation)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.InstallConversation(t.Context(), conversation); err != nil {
		t.Fatal(err)
	}
	return engine, set
}

func TestHandoffResumesPendingWebCall(t *testing.T) {
	backend := &handoffBackend{history: webHandoffHistory(`{"reason":"needs local files"}`)}
	engine, set := loadHandoff(t, backend)
	if len(set.Definitions()) != 0 {
		t.Fatalf("handoff is advertised: %+v", set.Definitions())
	}
	if !engine.CanResumeTools(set) {
		t.Fatal("pending handoff is not resumable")
	}
	events, unsubscribe := engine.Subscribe()
	defer unsubscribe()
	run, err := engine.ResumePendingTools(t.Context(), agent.TurnInput{Tools: set, OnDeny: agent.DenyContinue})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range agent.Follow(events, run) {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
	}
	if backend.sends != 1 || len(backend.responses) != 1 {
		t.Fatalf("sends = %d, responses = %+v", backend.sends, backend.responses)
	}
	response := backend.responses[0]
	if response.ToolCallID != "handoff-1" || response.Status != assistant.ToolStatusSuccess {
		t.Fatalf("response = %+v, want success for handoff-1", response)
	}
	output := response.Metadata.Output
	if output != handoffAccepted {
		t.Fatalf("output = %q", output)
	}
	for _, tool := range backend.tools {
		if tool.Name == spec.Handoff {
			t.Fatal("resumed request advertised the handoff tool")
		}
	}
}

func TestHandoffWithAnyInputStillSucceeds(t *testing.T) {
	for _, input := range []string{`{}`, `not json`} {
		result := handoff(agent.ToolCall{ID: "handoff-1", Name: spec.Handoff, Input: input})
		if result.IsError || result.Output != handoffAccepted {
			t.Fatalf("input %q: result = %+v", input, result)
		}
	}
}

func TestRejectedHandoffHasNothingToResume(t *testing.T) {
	history := webHandoffHistory(`{}`)
	rejected := assistant.AssistantMessage("rejected", assistant.ToolResultContent("handoff-1", spec.Handoff, assistant.ToolStatusError, "The user cancelled the handoff."))
	rejected.Role = "user"
	rejected.Content.Type = assistant.ContentClientToolResponse
	backend := &handoffBackend{history: append(history, rejected)}
	engine, set := loadHandoff(t, backend)
	if engine.CanResumeTools(set) {
		t.Fatal("rejected handoff is resumable")
	}
	if backend.sends != 0 {
		t.Fatalf("sends = %d, want none", backend.sends)
	}
}

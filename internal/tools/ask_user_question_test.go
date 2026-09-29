package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/workspace"
)

const validQuestion = `{"questions":[{"question":"Which?","options":[{"label":"A","description":"Option A"}]}]}`

type headlessQuestionBackend struct {
	t        *testing.T
	calls    int
	response assistant.ClientToolResponse
}

func (b *headlessQuestionBackend) Send(_ context.Context, message any, opts assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.calls++
	for _, def := range opts.ClientTools {
		if def.Name == spec.AskUserQuestion {
			b.t.Error("headless advertised ask_user_question")
		}
	}
	if b.calls == 1 {
		content := assistant.ToolCallContent("question", spec.AskUserQuestion, validQuestion)
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("question", content)
		return "conversation", emit(response)
	}
	responses, ok := message.([]assistant.ClientToolResponse)
	if !ok || len(responses) != 1 {
		b.t.Errorf("unexpected responses: %v", message)
		return "conversation", nil
	}
	b.response = responses[0]
	return "conversation", nil
}

func TestHeadlessUnexpectedQuestionReturnsUnsupported(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	set, err := agent.NewToolSet(agent.ModeSkipPermissions, NewClientTools(ws)...)
	if err != nil {
		t.Fatal(err)
	}
	b := &headlessQuestionBackend{t: t}
	engine := agent.New(b, assistant.SendOptions{})
	result, err := engine.RunTurn(t.Context(), agent.TurnInput{Message: "choose", Tools: set}, nil)
	if err != nil || result.Outcome != agent.TurnOutcomeCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if b.calls != 2 || b.response.Status != assistant.ToolStatusError || !strings.Contains(b.response.Metadata.Output, "no client tool named") {
		t.Fatalf("calls=%d response=%+v", b.calls, b.response)
	}
}

type failingInteractor struct{ t *testing.T }

func (f failingInteractor) Interact(context.Context, agent.ToolCall) (agent.ToolResult, error) {
	f.t.Error("malformed arguments opened the question form")
	return agent.ToolResult{}, nil
}

func TestQuestionMalformedArgumentsReturnErrorWithoutForm(t *testing.T) {
	tool := NewAskUserQuestionTool(failingInteractor{t})
	result, err := tool.Handler(t.Context(), agent.ToolCall{Name: spec.AskUserQuestion, Input: `{"questions":[]}`})
	if err != nil || !result.IsError || !strings.Contains(result.Output, "invalid ask_user_question arguments") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

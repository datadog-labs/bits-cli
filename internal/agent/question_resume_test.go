package agent

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
)

func TestPendingQuestionRequiresFinalUnansweredCall(t *testing.T) {
	call := assistant.ToolCallContent("question-1", spec.AskUserQuestion, `{"questions":[{"question":"Which region?","options":[{"label":"US","description":"United States"}]}]}`)
	call.Type = assistant.ContentClientToolCall
	question := assistant.AssistantMessage("call", call)
	response := assistant.AssistantMessage("response", assistant.ToolResultContent("question-1", spec.AskUserQuestion, assistant.ToolStatusSuccess, "answered"))
	stopped := assistant.AssistantMessage("stop", assistant.Content{Type: assistant.ContentUserStop})
	invalid := assistant.ToolCallContent("question-2", spec.AskUserQuestion, `{"questions":[]}`)
	invalid.Type = assistant.ContentClientToolCall
	other := assistant.ToolCallContent("other", "read_file", `{}`)
	other.Type = assistant.ContentClientToolCall

	for _, tc := range []struct {
		name     string
		messages []assistant.Message
		want     bool
	}{
		{"unanswered", []assistant.Message{question}, true},
		{"answered", []assistant.Message{question, response}, false},
		{"stopped", []assistant.Message{question, stopped}, false},
		{"later message", []assistant.Message{question, assistant.AssistantMessage("later", assistant.TextContent("Done."))}, false},
		{"invalid input", []assistant.Message{assistant.AssistantMessage("invalid", invalid)}, false},
		{"different tool", []assistant.Message{assistant.AssistantMessage("other", other)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pendingQuestionFromHistory(tc.messages)
			if (got != nil) != tc.want {
				t.Fatalf("pending question = %v, want %v", got != nil, tc.want)
			}
		})
	}
}

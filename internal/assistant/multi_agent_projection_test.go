package assistant_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestMultiAgentProjectionPreservesTranscriptPayloads(t *testing.T) {
	const localOutput = `{"success":true,"answers":["yes"],"message":"Answered"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Assistant-Multi-Agent-Run-ID", "run")
		w.Header().Set("X-Assistant-Conversation-ID", "conversation")
		w.Header().Set("X-Assistant-Multi-Agent-Root-Agent-ID", "root")
		w.Header().Set("Content-Type", "text/event-stream")
		for i, event := range []struct{ kind, body string }{
			{"model.response_started", `{}`},
			{"content.fragment_received", `{"fragment":"before "}`},
			{"tool.call_requested", `{"tool_call_id":"question","tool_name":"ask_user_question","tool_input":"{}","is_client_side":true}`},
			{"client_tool.batch_ready", `{"generation":1,"tool_call_ids":["question"]}`},
			{"tool.call_completed", `{"tool_call_id":"question","tool_name":"ask_user_question","status":"success","display_content":"Answered"}`},
			{"content.widget_generated", `{"widget_type":"first","widget_content":{"title":"first"}}`},
			{"content.widget_generated", `{"widget_type":"second","widget_content":{"title":"second"}}`},
			{"content.dashboard_generated", `{"dashboard_content":{"title":"first"}}`},
			{"content.dashboard_generated", `{"dashboard_content":{"title":"second"}}`},
			{"content.fragment_received", `{"fragment":"after"}`},
			{"run.terminal", `{"outcome":"completed"}`},
		} {
			_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":\"conversation\",\"run_id\":\"run\",\"agent_id\":\"root\",\"sequence\":%d,\"type\":%q,\"event\":%s}\n\n", i+1, event.kind, event.body)
		}
	}))
	defer server.Close()
	transcript := agent.NewTranscript()
	backend := assistant.NewMultiAgentClient(&assistant.Client{BaseURL: server.URL, HTTPClient: server.Client()})
	err := backend.Stream(context.Background(), "question", assistant.SendOptions{}, func(response assistant.AssistantResponse) error {
		message := response.Data.Attributes.StructuredMessage
		transcript.AppendMessage(message)
		if message.Content.Type == assistant.ContentClientToolCall {
			transcript.MarkToolExecuted(message.Content.Tool.ToolCallID, agent.ToolResult{Output: localOutput, Display: "Answered"})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var widgets, dashboards []string
	var text string
	for _, block := range transcript.Blocks() {
		if block.Tool != nil && (block.Tool.Output != localOutput || block.Tool.Detail != "Answered") {
			t.Errorf("local result changed: %+v", block.Tool)
		}
		if block.Widget != nil {
			widgets = append(widgets, block.Widget.Title)
		}
		if block.Dashboard != nil {
			dashboards = append(dashboards, block.Dashboard.Title)
		}
		if block.Markdown != nil {
			text += block.Markdown.Content
		}
	}
	if fmt.Sprint(widgets) != "[first second]" || fmt.Sprint(dashboards) != "[first second]" {
		t.Errorf("visualizations lost: widgets=%v dashboards=%v", widgets, dashboards)
	}
	if text != "before after" {
		t.Errorf("streamed text = %q", text)
	}
}

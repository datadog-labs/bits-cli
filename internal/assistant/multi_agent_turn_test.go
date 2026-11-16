package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMultiAgentRootTurnCompletionKeepsStreamOpen(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != multiAgentRunsPath {
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set(multiAgentRunIDHeader, multiAgentRunID)
		w.Header().Set(multiAgentRootIDHeader, "root")
		events := []struct{ agent, kind, event string }{
			{"root", "model.response_started", `{}`},
			{"root", "content.fragment_received", `{"fragment":"initial"}`},
			{"root", "assistant.turn_completed", `{}`},
			{"child", "tool.call_requested", `{"tool_call_id":"call","tool_name":"read_file","tool_input":"{}"}`},
			{"child", "client_tool.batch_ready", `{"generation":1,"tool_call_ids":["call"]}`},
			{"child", "assistant.turn_completed", `{}`},
			{"root", "model.response_started", `{}`},
			{"root", "content.fragment_received", `{"fragment":"followup"}`},
			{"root", "assistant.turn_completed", `{}`},
			{"", "run.terminal", `{"outcome":"completed"}`},
		}
		for i, e := range events {
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":%q,"sequence":%d,"type":%q,"event":%s}`, multiAgentRunID, e.agent, i+1, e.kind, e.event))
		}
		// Successful terminal handling must return without waiting for HTTP EOF.
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var order, ids []string
	err := NewMultiAgentClient(c).Stream(ctx, "prompt", SendOptions{}, func(response AssistantResponse) error {
		if response.Data.Attributes.ConversationID != multiAgentRunID {
			t.Errorf("conversation ID = %q", response.Data.Attributes.ConversationID)
		}
		message := response.Data.Attributes.StructuredMessage
		if response.RootTurnCompleted {
			if message.MessageID != "" || message.Content.Type != "" || response.Data.ID != "" {
				t.Error("completion must not manufacture content")
			}
			order = append(order, "completed")
			return nil
		}
		if message.MessageID == "" {
			return nil
		}
		if message.Content.Type == ContentClientToolCall {
			order = append(order, "child tool")
			return nil
		}
		order = append(order, "text")
		ids = append(ids, message.MessageID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "text,completed,child tool,text,completed" {
		t.Fatalf("callback order = %s", got)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("root response IDs = %v", ids)
	}
}

func TestMultiAgentToolIdentitiesIncludeRun(t *testing.T) {
	cases := []struct {
		name   string
		events []multiAgentEnvelope
	}{
		{"preview", []multiAgentEnvelope{
			{Type: "tool.call_started", Event: json.RawMessage(`{"tool_call_id":"wire","tool_name":"read_file","is_client_side":true}`)},
			{Type: "tool.call_requested", Event: json.RawMessage(`{"tool_call_id":"wire","tool_name":"read_file","tool_input":"{}"}`)},
			{Type: "client_tool.batch_ready", Event: json.RawMessage(`{"generation":1,"tool_call_ids":["wire"]}`)},
			{Type: "tool.call_completed", Event: json.RawMessage(`{"tool_call_id":"wire","tool_name":"read_file","status":"success"}`)},
		}},
		{"execution", []multiAgentEnvelope{
			{Type: "tool.call_requested", Event: json.RawMessage(`{"tool_call_id":"wire","tool_name":"read_file","tool_input":"{}"}`)},
			{Type: "client_tool.batch_ready", Event: json.RawMessage(`{"generation":1,"tool_call_ids":["wire"]}`)},
		}},
		{"server", []multiAgentEnvelope{
			{Type: "tool.call_requested", Event: json.RawMessage(`{"tool_call_id":"wire","tool_name":"read_file","is_client_side":false,"tool_input":"{}"}`)},
		}},
		{"completion_without_preview", []multiAgentEnvelope{
			{Type: "tool.call_completed", Event: json.RawMessage(`{"tool_call_id":"wire","tool_name":"read_file","status":"success"}`)},
		}},
		{"approval", []multiAgentEnvelope{
			{Type: "tool.call_requested", Event: json.RawMessage(fmt.Sprintf(`{"tool_call_id":"wire","tool_name":%q,"tool_input":"{\"tool_call_id\":\"wire\",\"tool_name\":\"write_file\",\"tool_args\":{}}"}`, ApprovalRequestTool))},
			{Type: "client_tool.batch_ready", Event: json.RawMessage(`{"generation":1,"tool_call_ids":["wire"]}`)},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := map[string]bool{}
			for _, runID := range []string{"run-a", "run-b"} {
				run := &multiAgentRun{id: runID, messageIDs: map[string]string{}, requestedTools: map[multiAgentToolKey]multiAgentToolRequested{}, pendingTools: map[string]multiAgentToolCorrelation{}, toolIDs: map[multiAgentToolKey]string{}}
				var localID string
				for i, event := range tc.events {
					event.AgentID = "root"
					event.Sequence = i + 1
					_, err := (&MultiAgentClient{}).handleEvent(run, event, func(response AssistantResponse) error {
						tool := response.Data.Attributes.StructuredMessage.Content.Tool
						if tool == nil {
							t.Fatal("missing tool")
						}
						if localID != "" && localID != tool.ToolCallID {
							t.Fatalf("preview/execution/completion IDs differ: %s vs %s", localID, tool.ToolCallID)
						}
						localID = tool.ToolCallID
						if tc.name == "approval" {
							var input struct {
								ToolCallID string `json:"tool_call_id"`
							}
							if err := json.Unmarshal([]byte(tool.Metadata.Input), &input); err != nil {
								t.Fatal(err)
							}
							if input.ToolCallID != localID {
								t.Fatalf("approval ID = %s, want %s", input.ToolCallID, localID)
							}
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				if !strings.HasPrefix(localID, "multi-agent:"+runID+":") {
					t.Fatalf("tool ID missing run namespace: %s", localID)
				}
				if ids[localID] {
					t.Fatalf("overlapping runs reused tool ID: %s", localID)
				}
				ids[localID] = true
			}
		})
	}
}

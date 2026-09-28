package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func savedCall(id, name string) assistant.Message {
	call := assistant.ToolCallContent(id, name, `{}`)
	call.Type = assistant.ContentClientToolCall
	return assistant.AssistantMessage(id, call)
}

func TestContinuationHistoryBoundaries(t *testing.T) {
	first, second := savedCall("one", "input"), savedCall("two", "input")
	response := assistant.AssistantMessage("result", assistant.ToolResultContent("one", "input", assistant.ToolStatusSuccess, "done"))
	response.Role = "user"
	response.Content.Type = assistant.ContentClientToolResponse
	stop := assistant.AssistantMessage("stop", assistant.Content{Type: assistant.ContentUserStop})
	user := assistant.Message{Role: "user", Content: assistant.TextContent("new turn")}
	for _, tc := range []struct {
		name     string
		messages []assistant.Message
		ids      []string
	}{
		{"batch with bookkeeping", []assistant.Message{first, second, {Results: &assistant.Results{}}, assistant.AssistantMessage("end", assistant.Content{Type: assistant.ContentTurnStatus}), assistant.AssistantMessage("internal", assistant.Content{Type: assistant.ContentProviderCompaction})}, []string{"one", "two"}},
		{"partial response", []assistant.Message{first, second, response}, []string{"two"}},
		{"answered", []assistant.Message{first, response}, nil},
		{"stopped", []assistant.Message{first, second, stop}, nil},
		{"new user turn", []assistant.Message{first, user}, nil},
		{"duplicate final call", []assistant.Message{first, first}, []string{"one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending := continuationFromHistory(tc.messages)
			var ids []string
			if pending != nil {
				for _, c := range pending.calls {
					ids = append(ids, c.Tool.ToolCallID)
				}
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("pending IDs = %v, want %v", ids, tc.ids)
			}
		})
	}
}

func TestContinuationKeepsOriginalTurnContext(t *testing.T) {
	user := assistant.Message{Role: "user", Content: assistant.TextContent("investigate"), ContextEntities: json.RawMessage(`[{"type":"service","id":"api","label":"API"}]`), ContextResources: json.RawMessage(`[{"name":"dashboard","value":42}]`)}
	c := continuationFromHistory([]assistant.Message{user, savedCall("one", "input")})
	if c.err != nil || c.context == nil || c.context.Entities[0].ID != "api" || len(c.context.Resources) != 1 {
		t.Fatalf("context = %+v", c)
	}
	next := assistant.Message{Role: "user", Content: assistant.TextContent("new turn")}
	c = continuationFromHistory([]assistant.Message{user, savedCall("one", "input"), next, savedCall("two", "input")})
	if c.context != nil {
		t.Fatal("context leaked across user turns")
	}
	user.ContextEntities = json.RawMessage(`{"unexpected":"shape"}`)
	c = continuationFromHistory([]assistant.Message{user, savedCall("one", "input")})
	if c.err == nil {
		t.Fatal("malformed context was silently discarded")
	}
}

func TestResumeRequiresEveryHandlerToOptIn(t *testing.T) {
	handler := func(context.Context, ToolCall) (ToolResult, error) {
		t.Error("unsafe handler executed")
		return ToolResult{}, nil
	}
	set, err := NewToolSet(ModeSkipPermissions,
		Tool{Definition: assistant.ClientTool{Name: "input"}, Handler: handler, Resumable: true},
		Tool{Definition: assistant.ClientTool{Name: "write"}, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"write", "unknown", assistant.ApprovalRequestTool} {
		e := New(&scriptBackend{}, assistant.SendOptions{})
		e.continuation = continuationFromHistory([]assistant.Message{savedCall("one", "input"), savedCall("two", name)})
		if e.CanResumeTools(set) {
			t.Fatalf("batch containing %s is resumable", name)
		}
		events := drain(e.ResumePendingTools(t.Context(), TurnInput{Tools: set, Interactive: true}))
		if len(events) != 1 || !errors.Is(events[0].Err, ErrNoPendingTools) {
			t.Fatalf("events = %+v", events)
		}
	}
}

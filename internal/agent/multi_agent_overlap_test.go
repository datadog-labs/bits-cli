package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestMultiAgentOverlappingHTTPStreamsRouteChildTools(t *testing.T) {
	const conversation = "12345678-1234-1234-1234-123456789abc"
	secondStarted, finishSecond := make(chan struct{}), make(chan struct{})
	answers := map[string]chan struct{}{"run-one": make(chan struct{}), "run-two": make(chan struct{})}
	var mu sync.Mutex
	results := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tool-results") {
			var body struct {
				Results []struct {
					RunID      string `json:"run_id"`
					ToolCallID string `json:"tool_call_id"`
					Output     string `json:"output"`
				} `json:"results"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Results) != 1 {
				t.Errorf("invalid submission: %+v %v", body, err)
				w.WriteHeader(400)
				return
			}
			result := body.Results[0]
			if r.URL.Path != "/api/unstable/assistant/multi-agent/runs/"+result.RunID+"/tool-results" || result.ToolCallID != "same-wire-id" {
				t.Errorf("misrouted result: %s %+v", r.URL.Path, result)
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			results[result.RunID] = result.Output
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			close(answers[result.RunID])
			return
		}
		id := "run-one"
		if r.URL.Path == "/api/unstable/assistant/multi-agent/conversation/"+conversation {
			id = "run-two"
			close(secondStarted)
		} else if r.URL.Path != "/api/unstable/assistant/multi-agent/runs" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("X-Assistant-Multi-Agent-Run-ID", id)
		w.Header().Set("X-Assistant-Multi-Agent-Root-Agent-ID", "root")
		w.Header().Set("X-Assistant-Conversation-ID", conversation)
		w.Header().Set("Content-Type", "text/event-stream")
		sequence := 0
		send := func(agentID, kind string, event map[string]any) {
			sequence++
			payload, _ := json.Marshal(map[string]any{"conversation_id": conversation, "run_id": id, "agent_id": agentID, "sequence": sequence, "type": kind, "event": event})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			w.(http.Flusher).Flush()
		}
		wait := func(ch <-chan struct{}) bool {
			select {
			case <-ch:
				return true
			case <-r.Context().Done():
				return false
			}
		}
		send("root", "content.fragment_received", map[string]any{"fragment": id + " initial"})
		if id == "run-one" {
			send("root", "assistant.turn_completed", map[string]any{})
			if !wait(secondStarted) {
				return
			}
		}
		send("child", "tool.call_requested", map[string]any{"tool_call_id": "same-wire-id", "tool_name": "read", "tool_input": "{}", "is_client_side": true})
		send("child", "client_tool.batch_ready", map[string]any{"generation": 1, "tool_call_ids": []string{"same-wire-id"}})
		if !wait(answers[id]) {
			return
		}
		if id == "run-two" && !wait(finishSecond) {
			return
		}
		send("root", "model.response_started", map[string]any{})
		send("root", "content.fragment_received", map[string]any{"fragment": id + " final"})
		send("root", "assistant.turn_completed", map[string]any{})
		send("", "run.terminal", map[string]any{"outcome": "completed", "code": "completed"})
	}))
	defer server.Close()
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(_ context.Context, call ToolCall) (ToolResult, error) { return ToolResult{Output: call.ID}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(assistant.NewMultiAgentClient(&assistant.Client{BaseURL: server.URL, HTTPClient: server.Client()}), Config{})
	defer engine.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, unsubscribe := engine.Subscribe()
	defer unsubscribe()
	first, err := engine.StartTurn(ctx, TurnInput{Message: "first", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	backgroundEvent(t, events, first, EventBackground)
	second, err := engine.StartTurn(ctx, TurnInput{Message: "second", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	backgroundEvent(t, events, first, EventDone)
	if !engine.OperationActive() {
		t.Fatal("first run ended second operation")
	}
	close(finishSecond)
	backgroundEvent(t, events, second, EventDone)
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"run-one", "run-two"} {
		if !strings.Contains(results[id], id) {
			t.Errorf("result not scoped to %s: %q", id, results[id])
		}
	}
	texts := map[string]bool{}
	for _, block := range engine.State().Blocks {
		if block.Markdown != nil {
			texts[block.Markdown.Content] = true
		}
	}
	for _, text := range []string{"run-one initial", "run-one final", "run-two initial", "run-two final"} {
		if !texts[text] {
			t.Errorf("missing %q in %v", text, texts)
		}
	}
}

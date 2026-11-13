package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/headless"
)

type multiAgentStreamTransport struct{ body io.ReadCloser }

func (s multiAgentStreamTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"X-Assistant-Multi-Agent-Run-Id":        {"review-run"},
		"X-Assistant-Conversation-Id":           {"review-conversation"},
		"X-Assistant-Multi-Agent-Root-Agent-Id": {"opaque-root-id"},
		"Content-Type":                          {"text/event-stream"},
	}, Body: s.body}, nil
}

func TestMultiAgentFinalAnswerUsesServerDeclaredRoot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer func() { _ = reader.Close() }()
		defer func() { _ = writer.Close() }()
		backend := assistant.NewMultiAgentClient(&assistant.Client{BaseURL: "https://assistant.test", HTTPClient: &http.Client{Transport: multiAgentStreamTransport{reader}}})
		engine := agent.New(backend, agent.Config{PermissionsMode: agent.ModeSkipPermissions})
		defer engine.Close()
		tools, err := agent.NewToolSet()
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runEngineTurn(context.Background(), engine, agent.TurnInput{Tools: tools}, runOptions(), &out)
		}()
		sequence := 0
		send := func(agentID, kind, event string) {
			sequence++
			if _, err := fmt.Fprintf(writer, "data: {\"conversation_id\":\"review-conversation\",\"run_id\":\"review-run\",\"agent_id\":%q,\"sequence\":%d,\"type\":%q,\"event\":%s}\n\n", agentID, sequence, kind, event); err != nil {
				t.Fatal(err)
			}
		}
		send("child-1", "content.fragment_received", `{"fragment":"Tentative child answer: broken database."}`)
		send("opaque-root-id", "content.fragment_received", `{"fragment":"Initial root answer."}`)
		send("opaque-root-id", "assistant.turn_completed", `{}`)
		synctest.Wait()
		if strings.Contains(out.String(), `"type":"run.finished"`) {
			t.Fatal("delivery finished at root marker")
		}
		select {
		case err := <-done:
			t.Fatalf("headless run returned at root marker: %v", err)
		default:
		}
		// The stream stays held at the marker until all consumers are blocked.
		send("opaque-root-id", "model.response_started", `{}`)
		send("opaque-root-id", "content.fragment_received", `{"fragment":"Verified final answer: database is healthy."}`)
		send("child-1", "content.fragment_received", `{"fragment":" Child follow-up must remain excluded."}`)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("headless run returned before terminal: %v", err)
		default:
		}
		send("", "run.terminal", `{"outcome":"completed"}`)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		finished := 0
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			if record["type"] == "run.finished" {
				finished++
				if record["response"] != "Initial root answer.\n\nVerified final answer: database is healthy." {
					t.Fatalf("final root answer = %q", record["response"])
				}
			}
		}
		if finished != 1 {
			t.Fatalf("run.finished records = %d, want 1", finished)
		}
	})
}

func TestMultiAgentSiblingToolsOverlapAcrossHTTPBatches(t *testing.T) {
	var started, posted atomic.Int32
	bothStarted := make(chan struct{})
	bothPosted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tool-results") {
			var payload struct {
				Results []struct {
					AgentID    string `json:"agent_id"`
					Generation int    `json:"generation"`
					ToolCallID string `json:"tool_call_id"`
					Status     string `json:"status"`
				} `json:"results"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if len(payload.Results) != 1 {
				t.Errorf("expected independent result submission, got %d", len(payload.Results))
				w.WriteHeader(400)
				return
			}
			result := payload.Results[0]
			if result.ToolCallID != "same-wire-id" || result.Generation != 1 || result.Status != "success" || (result.AgentID != "child-1" && result.AgentID != "child-2") {
				t.Errorf("incorrect result correlation: %+v", result)
			}
			_, _ = fmt.Fprint(w, `{"accepted":1,"duplicates":0}`)
			if posted.Add(1) == 2 {
				close(bothPosted)
			}
			return
		}
		w.Header().Set("X-Assistant-Multi-Agent-Run-ID", "review-run")
		w.Header().Set("X-Assistant-Conversation-ID", "review-conversation")
		w.Header().Set("X-Assistant-Multi-Agent-Root-Agent-ID", "opaque-root-id")
		w.Header().Set("Content-Type", "text/event-stream")
		seq := 0
		for _, id := range []string{"child-1", "child-2"} {
			seq++
			_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":\"review-conversation\",\"run_id\":\"review-run\",\"agent_id\":%q,\"sequence\":%d,\"type\":\"tool.call_requested\",\"event\":{\"tool_call_id\":\"same-wire-id\",\"tool_name\":\"barrier\",\"tool_input\":\"{}\"}}\n\n", id, seq)
			seq++
			_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":\"review-conversation\",\"run_id\":\"review-run\",\"agent_id\":%q,\"sequence\":%d,\"type\":\"client_tool.batch_ready\",\"event\":{\"generation\":1,\"tool_call_ids\":[\"same-wire-id\"]}}\n\n", id, seq)
			w.(http.Flusher).Flush()
		}
		select {
		case <-bothPosted:
			_, _ = fmt.Fprint(w, "data: {\"conversation_id\":\"review-conversation\",\"run_id\":\"review-run\",\"agent_id\":null,\"sequence\":5,\"type\":\"run.terminal\",\"event\":{\"outcome\":\"completed\"}}\n\n")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	backend := assistant.NewMultiAgentClient(&assistant.Client{BaseURL: server.URL, HTTPClient: server.Client()})
	engine := agent.New(backend, agent.Config{PermissionsMode: agent.ModeSkipPermissions})
	tools, err := agent.NewToolSet(agent.Tool{
		Definition: assistant.ClientTool{Name: "barrier"},
		Handler: func(ctx context.Context, _ agent.ToolCall) (agent.ToolResult, error) {
			if started.Add(1) == 2 {
				close(bothStarted)
			}
			select {
			case <-bothStarted:
				return agent.ToolResult{Output: "ok"}, nil
			case <-ctx.Done():
				return agent.ToolResult{}, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = headless.Run(ctx, engine, agent.TurnInput{Message: "test", Tools: tools}, &multiAgentRecordingDelivery{})
	if err != nil {
		t.Fatalf("sibling calls could not overlap: started=%d, error=%v", started.Load(), err)
	}
	if posted.Load() != 2 {
		t.Fatalf("posted %d results, want 2", posted.Load())
	}
}

func TestMultiAgentServerApprovalSupportsRepeatedDeferral(t *testing.T) {
	posted := make(chan int, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tool-results") {
			var payload struct {
				Results []struct {
					Generation int    `json:"generation"`
					ToolCallID string `json:"tool_call_id"`
					Status     string `json:"status"`
					Output     string `json:"output"`
				} `json:"results"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if len(payload.Results) != 1 {
				t.Error("missing approval response")
				w.WriteHeader(400)
				return
			}
			result := payload.Results[0]
			if result.ToolCallID != "approval-wire-id" || result.Status != "success" || result.Output != "the write was approved" {
				t.Errorf("approval response = %+v", result)
			}
			posted <- result.Generation
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Assistant-Conversation-ID", "approval-conversation")
		w.Header().Set("X-Assistant-Multi-Agent-Run-ID", "approval-run")
		w.Header().Set("X-Assistant-Multi-Agent-Root-Agent-ID", "root")
		w.Header().Set("Content-Type", "text/event-stream")
		sequence := 0
		emit := func(typ string, event any) {
			sequence++
			raw, err := json.Marshal(event)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":\"approval-conversation\",\"run_id\":\"approval-run\",\"agent_id\":\"root\",\"sequence\":%d,\"type\":%q,\"event\":%s}\n\n", sequence, typ, raw)
			w.(http.Flusher).Flush()
		}
		for generation := 1; generation <= 2; generation++ {
			emit("tool.call_requested", map[string]any{
				"tool_call_id": "approval-wire-id", "tool_name": "approval_request",
				"tool_input": `{"tool_name":"write_dashboard","tool_args":{"title":"CPU"},"tool_call_id":"approval-wire-id","approval_message":"Publish?"}`,
			})
			emit("client_tool.batch_ready", map[string]any{"generation": generation, "tool_call_ids": []string{"approval-wire-id"}})
			select {
			case got := <-posted:
				if got != generation {
					t.Errorf("generation = %d, want %d", got, generation)
				}
			case <-r.Context().Done():
				return
			}
		}
		emit("run.terminal", map[string]any{"outcome": "completed"})
	}))
	defer server.Close()
	engine := agent.New(assistant.NewMultiAgentClient(&assistant.Client{BaseURL: server.URL, HTTPClient: server.Client()}), agent.Config{PermissionsMode: agent.ModeSkipPermissions})
	tools, err := agent.NewToolSet()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = headless.Run(ctx, engine, agent.TurnInput{Message: "approve", Tools: tools}, &multiAgentRecordingDelivery{})
	if err != nil {
		t.Fatal(err)
	}
	if engine.State().ConversationID != "approval-conversation" {
		t.Fatalf("conversation = %q", engine.State().ConversationID)
	}
}

type multiAgentRecordingDelivery struct {
	finish headless.Finish
}

func (*multiAgentRecordingDelivery) Start(headless.Start) error { return nil }
func (*multiAgentRecordingDelivery) Consume(agent.Event) error  { return nil }
func (d *multiAgentRecordingDelivery) Finish(finish headless.Finish) error {
	d.finish = finish
	return nil
}

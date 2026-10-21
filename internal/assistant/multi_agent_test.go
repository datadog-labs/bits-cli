package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const multiAgentRunID = "12345678-1234-1234-1234-123456789abc"

func TestMultiAgentClientRequiresRootIdentity(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(multiAgentRunIDHeader, multiAgentRunID)
		w.WriteHeader(http.StatusOK)
	})
	err := NewMultiAgentClient(c).Stream(context.Background(), "investigate", SendOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), multiAgentRootIDHeader) {
		t.Fatalf("missing root header error = %v", err)
	}
}

func TestMultiAgentClientTerminalFailureDoesNotCancelAgain(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != multiAgentRunsPath {
			t.Errorf("terminal run triggered another request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set(multiAgentRunIDHeader, multiAgentRunID)
		w.Header().Set(multiAgentRootIDHeader, "opaque-root")
		writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":null,"sequence":1,"type":"run.terminal","event":{"outcome":"failed","code":"agent_failed","message":"failure detail"}}`, multiAgentRunID))
	})
	err := NewMultiAgentClient(c).Stream(context.Background(), "investigate", SendOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "failure detail") {
		t.Fatalf("terminal failure = %v", err)
	}
}

func writeSSEEvent(t *testing.T, w http.ResponseWriter, payload string) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope["conversation_id"]; !ok {
		envelope["conversation_id"] = w.Header().Get(multiAgentRunIDHeader)
	}
	w.Header().Set(multiAgentConversationIDHeader, fmt.Sprint(envelope["conversation_id"]))
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestMultiAgentClientRunsToolsThroughCorrelatedEndpoint(t *testing.T) {
	toolResultsPosted := make(chan struct{})
	var once sync.Once
	var gotStart struct {
		Prompt            string          `json:"prompt"`
		Profile           Profile         `json:"profile"`
		Model             string          `json:"model"`
		Context           json.RawMessage `json:"context"`
		CustomUserContext string          `json:"custom_user_context"`
		ClientTools       []ClientTool    `json:"client_tools"`
		SkillOverrides    []SkillOverride `json:"skill_overrides"`
	}
	var gotResults struct {
		Results []struct {
			RunID      string `json:"run_id"`
			AgentID    string `json:"agent_id"`
			Generation int    `json:"generation"`
			ToolCallID string `json:"tool_call_id"`
			Status     string `json:"status"`
			Output     string `json:"output"`
			Title      string `json:"title"`
			Content    string `json:"content"`
		} `json:"results"`
	}

	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/unstable/assistant/multi-agent/runs":
			if err := json.NewDecoder(r.Body).Decode(&gotStart); err != nil {
				t.Errorf("decode start: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Assistant-Multi-Agent-Run-ID", multiAgentRunID)
			w.Header().Set(multiAgentRootIDHeader, "root")
			w.Header().Set(multiAgentConversationIDHeader, multiAgentRunID)
			w.WriteHeader(http.StatusOK)
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"root","sequence":1,"type":"model.response_started","event":{"type":"model.response_started","model_provider":"anthropic","model_name":"claude"}}`, multiAgentRunID))
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"root","sequence":2,"type":"content.fragment_received","event":{"type":"content.fragment_received","fragment":"checking "}}`, multiAgentRunID))
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"child-1","sequence":3,"type":"tool.call_requested","event":{"type":"tool.call_requested","tool_call_id":"call-1","tool_name":"read_file","tool_input":"{\"path\":\"README.md\"}","display_title":"Read README","display_content":"","arguments_truncated":false}}`, multiAgentRunID))
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"child-1","sequence":4,"type":"client_tool.batch_ready","event":{"type":"client_tool.batch_ready","generation":7,"tool_call_ids":["call-1"]}}`, multiAgentRunID))
			select {
			case <-toolResultsPosted:
			case <-r.Context().Done():
				return
			}
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"root","sequence":5,"type":"content.fragment_received","event":{"type":"content.fragment_received","fragment":"done"}}`, multiAgentRunID))
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"root","sequence":6,"type":"model.response_completed","event":{"type":"model.response_completed","model_provider":"anthropic","model_name":"claude","input_tokens":11,"output_tokens":5,"cache_read_tokens":0,"cache_write_tokens":0,"time_to_first_chunk_ms":9}}`, multiAgentRunID))
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":null,"sequence":7,"type":"run.terminal","event":{"type":"run.terminal","outcome":"completed","code":"completed","message":"Run completed."}}`, multiAgentRunID))
		case "/api/unstable/assistant/multi-agent/runs/" + multiAgentRunID + "/tool-results":
			if err := json.NewDecoder(r.Body).Decode(&gotResults); err != nil {
				t.Errorf("decode results: %v", err)
			}
			once.Do(func() { close(toolResultsPosted) })
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"accepted":1,"duplicates":0}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	backend := NewMultiAgentClient(c)
	opts := SendOptions{
		Model:             "claude-sonnet-4-6",
		Context:           &AssistantContext{Entities: []ContextEntity{{Type: EntityService, ID: "checkout"}}},
		CustomUserContext: "workspace context",
		SkillOverrides:    []SkillOverride{{Name: "coding", Source: "assistant", Enabled: false}},
		ClientTools: []ClientTool{{
			Name: "read_file", Description: "Read a file", InputSchema: map[string]any{"type": "object"},
		}},
	}

	var messages []Message
	err := backend.Stream(context.Background(), "investigate", opts, func(response AssistantResponse) error {
		if response.Data.Attributes.ConversationID != multiAgentRunID {
			t.Errorf("conversation ID = %q", response.Data.Attributes.ConversationID)
		}
		message := response.Data.Attributes.StructuredMessage
		if message.MessageID == "" {
			return nil
		}
		messages = append(messages, message)
		if message.Content.Type != ContentClientToolCall {
			return nil
		}
		localToolCallID := message.Content.Tool.ToolCallID
		if localToolCallID == "call-1" || localToolCallID == "" {
			t.Errorf("local tool call ID = %q, want a namespaced ID", localToolCallID)
		}
		result := ClientToolResponse{
			Type:       "client_tool_response",
			ToolCallID: localToolCallID,
			Title:      "README read",
			Status:     ToolStatusSuccess,
			Content:    &MarkdownContent{Type: "markdown_fragment", Content: "Read README.md"},
			Metadata:   ClientToolMetadata{Name: "read_file", Input: `{"path":"README.md"}`, Output: "contents"},
		}
		return backend.SubmitToolResults(context.Background(), []ClientToolResponse{result})
	})
	if err != nil {
		t.Fatalf("stream multi-agent run: %v", err)
	}
	if len(messages) != 4 || messages[0].Content.TextBody() != "checking " || messages[1].Content.Type != ContentClientToolCall || messages[2].Content.TextBody() != "done" || messages[3].Results == nil {
		t.Fatalf("stream messages = %#v", messages)
	}
	usage := messages[3].Results.Usage
	if usage == nil || usage.InputTokens == nil || *usage.InputTokens != 11 || usage.OutputTokens == nil || *usage.OutputTokens != 5 {
		t.Fatalf("usage = %#v", usage)
	}

	if gotStart.Profile != ProfileCLI || gotStart.Prompt != "investigate" || gotStart.Model != opts.Model || gotStart.CustomUserContext != "workspace context" || len(gotStart.ClientTools) != 1 {
		t.Fatalf("start payload = %#v", gotStart)
	}
	if len(gotStart.SkillOverrides) != 1 || gotStart.SkillOverrides[0] != opts.SkillOverrides[0] {
		t.Fatalf("skill overrides = %#v", gotStart.SkillOverrides)
	}
	if string(gotStart.Context) != `{"entities":[{"type":"service","id":"checkout"}]}` {
		t.Fatalf("start context = %s", gotStart.Context)
	}
	if len(gotResults.Results) != 1 {
		t.Fatalf("tool results = %#v", gotResults.Results)
	}
	got := gotResults.Results[0]
	if got.RunID != multiAgentRunID || got.AgentID != "child-1" || got.Generation != 7 || got.ToolCallID != "call-1" || got.Status != "success" || got.Output != "contents" || got.Title != "README read" || got.Content != "Read README.md" {
		t.Fatalf("tool result = %#v", got)
	}
}

func TestMultiAgentClientCancelsActiveRunWhenContextEnds(t *testing.T) {
	cancelled := make(chan struct{})
	streamReleased := make(chan struct{})
	var once sync.Once
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/unstable/assistant/multi-agent/runs":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Assistant-Multi-Agent-Run-ID", multiAgentRunID)
			w.Header().Set(multiAgentRootIDHeader, "root")
			w.Header().Set(multiAgentConversationIDHeader, multiAgentRunID)
			w.WriteHeader(http.StatusOK)
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"child-1","sequence":1,"type":"tool.call_requested","event":{"type":"tool.call_requested","tool_call_id":"call-1","tool_name":"read_file","tool_input":"{}","display_title":"Read","display_content":"","arguments_truncated":false}}`, multiAgentRunID))
			writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"child-1","sequence":2,"type":"client_tool.batch_ready","event":{"type":"client_tool.batch_ready","generation":1,"tool_call_ids":["call-1"]}}`, multiAgentRunID))
			select {
			case <-streamReleased:
			case <-r.Context().Done():
			}
		case "/api/unstable/assistant/multi-agent/runs/" + multiAgentRunID + "/cancel":
			once.Do(func() { close(cancelled) })
			close(streamReleased)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	backend := NewMultiAgentClient(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := backend.Stream(ctx, "investigate", SendOptions{}, func(AssistantResponse) error { cancel(); return nil })
	if err == nil {
		t.Fatal("cancelled run succeeded")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not call the cancel endpoint")
	}
}

func TestMultiAgentClientIgnoresUnknownAdditiveEvents(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/unstable/assistant/multi-agent/runs" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Assistant-Multi-Agent-Run-ID", multiAgentRunID)
		w.Header().Set(multiAgentRootIDHeader, "root")
		w.Header().Set(multiAgentConversationIDHeader, multiAgentRunID)
		w.WriteHeader(http.StatusOK)
		writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":"root","sequence":1,"type":"future.event","event":{"type":"future.event","value":true}}`, multiAgentRunID))
		writeSSEEvent(t, w, fmt.Sprintf(`{"run_id":%q,"agent_id":null,"sequence":2,"type":"run.terminal","event":{"type":"run.terminal","outcome":"completed","code":"completed","message":"Run completed."}}`, multiAgentRunID))
	})

	backend := NewMultiAgentClient(c)
	if err := backend.Stream(context.Background(), "investigate", SendOptions{}, nil); err != nil {
		t.Fatalf("unknown additive event broke the run: %v", err)
	}
}

func TestMultiAgentClientNamespacesToolCallsByAgentAndGeneration(t *testing.T) {
	run := &multiAgentRun{
		id:             multiAgentRunID,
		messageIDs:     make(map[string]string),
		requestedTools: make(map[multiAgentToolKey]multiAgentToolRequested),
		pendingTools:   make(map[string]multiAgentToolCorrelation),
		toolIDs:        make(map[multiAgentToolKey]string),
	}
	backend := &MultiAgentClient{}
	var localIDs []string
	emit := func(response AssistantResponse) error {
		if response.Data.Attributes.StructuredMessage.Content.Type == ContentClientToolCall {
			localIDs = append(localIDs, response.Data.Attributes.StructuredMessage.Content.Tool.ToolCallID)
		}
		return nil
	}

	sequence := 0
	emitBatch := func(agentID string, generation int) {
		t.Helper()
		sequence++
		requested := multiAgentEnvelope{
			RunID: multiAgentRunID, AgentID: agentID, Sequence: sequence, Type: "tool.call_requested",
			Event: json.RawMessage(`{"tool_call_id":"call-1","tool_name":"read_file","tool_input":"{}","display_title":"Read","display_content":"","arguments_truncated":false}`),
		}
		if _, err := backend.handleEvent(run, requested, emit); err != nil {
			t.Fatalf("request %s/%d: %v", agentID, generation, err)
		}
		sequence++
		batch := multiAgentEnvelope{
			RunID: multiAgentRunID, AgentID: agentID, Sequence: sequence, Type: "client_tool.batch_ready",
			Event: json.RawMessage(fmt.Sprintf(`{"generation":%d,"tool_call_ids":["call-1"]}`, generation)),
		}
		terminal, err := backend.handleEvent(run, batch, emit)
		if err != nil || terminal {
			t.Fatalf("batch %s/%d = terminal %v, error %v", agentID, generation, terminal, err)
		}
	}

	// A re-deferred call can reuse its wire ID without another preview.
	// Start at sequence 2 so the later generation 2 exercises both namespaces.
	sequence = 2
	if _, err := backend.handleEvent(run, multiAgentEnvelope{AgentID: "root", Sequence: sequence, Type: "tool.call_started", Event: json.RawMessage(`{"tool_call_id":"call-1","tool_name":"read_file","is_client_side":true}`)}, emit); err != nil {
		t.Fatal(err)
	}
	emitBatch("root", 1)
	emitBatch("child-1", 1)
	emitBatch("root", 2)
	if len(localIDs) != 3 || localIDs[0] == localIDs[1] || localIDs[0] == localIDs[2] || localIDs[1] == localIDs[2] {
		t.Fatalf("local tool IDs are not unique: %#v", localIDs)
	}
	if len(run.pendingTools) != 3 {
		t.Fatalf("pending tools = %#v, want three independently correlated calls", run.pendingTools)
	}
	for _, localID := range localIDs {
		correlation := run.pendingTools[localID]
		if correlation.wireToolCallID != "call-1" {
			t.Fatalf("correlation for %q = %#v", localID, correlation)
		}
	}
}

func TestMultiAgentClientIdleTimeout(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set(multiAgentRunIDHeader, multiAgentRunID)
		w.Header().Set(multiAgentRootIDHeader, "root")
		writeSSEEvent(t, w, fmt.Sprintf(`{"conversation_id":%q,"run_id":%q,"agent_id":"root","sequence":1,"type":"content.fragment_received","event":{"fragment":"first"}}`, multiAgentRunID, multiAgentRunID))
		<-r.Context().Done()
	})
	c.StreamIdleTimeout = 20 * time.Millisecond
	err := NewMultiAgentClient(c).Stream(context.Background(), "question", SendOptions{}, nil)
	if !errors.Is(err, ErrStreamIdleTimeout) || errors.Is(err, ErrServer) {
		t.Fatalf("idle timeout must be a local stream error, got %v", err)
	}
}

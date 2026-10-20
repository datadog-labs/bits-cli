package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMultiAgentRequestOptionsAndRoutingHeaders(t *testing.T) {
	for _, conversation := range []string{"", "existing"} {
		t.Run("conversation_"+conversation, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("test-drive-ds-rl") != "1" {
					t.Error("test-drive header missing")
				}
				var body multiAgentStartRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if !body.EnableDebugMode || body.DebugTag != "trace" || body.Referrer != "https://example.test/page" || body.ExperimentalToolOverrides["user_memory"] != true {
					t.Errorf("options = %+v", body)
				}
				want := multiAgentRunsPath
				if conversation != "" {
					want = multiAgentConversationPath + conversation
				}
				if r.URL.Path != want {
					t.Errorf("path = %s, want %s", r.URL.Path, want)
				}
				w.Header().Set(multiAgentRunIDHeader, multiAgentRunID)
				w.Header().Set(multiAgentRootIDHeader, "root")
				id := conversation
				if id == "" {
					id = multiAgentRunID
				}
				writeSSEEvent(t, w, fmt.Sprintf(`{"conversation_id":%q,"run_id":%q,"agent_id":"root","sequence":1,"type":"run.terminal","event":{"outcome":"completed"}}`, id, multiAgentRunID))
			})
			c.CustomHeaders = http.Header{"Test-Drive-Ds-Rl": []string{"1"}}
			opts := SendOptions{ConversationID: conversation, EnableDebugMode: true, DebugTag: "trace", Referrer: "https://example.test/page", ExperimentalToolOverrides: map[string]any{"user_memory": true}}
			if err := NewMultiAgentClient(c).Stream(context.Background(), "question", opts, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMultiAgentToolFollowupsKeepRoutingHeaders(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("test-drive-ds-rl") != "1" {
			t.Error("missing TD header")
		}
		if !strings.HasSuffix(r.URL.Path, "/tool-results") && !strings.HasSuffix(r.URL.Path, "/cancel") {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c.CustomHeaders = http.Header{"Test-Drive-Ds-Rl": []string{"1"}}
	b := NewMultiAgentClient(c)
	run := &multiAgentRun{id: multiAgentRunID, pendingTools: map[string]multiAgentToolCorrelation{"local": {agentID: "root", generation: 1, wireToolCallID: "wire"}}}
	if err := b.submitToolResults(context.Background(), run, []ClientToolResponse{{ToolCallID: "local", Status: ToolStatusSuccess}}); err != nil {
		t.Fatal(err)
	}
	if err := b.cancelRun(context.Background(), multiAgentRunID); err != nil {
		t.Fatal(err)
	}
}

func TestMultiAgentPublicEventParity(t *testing.T) {
	run := &multiAgentRun{id: multiAgentRunID, rootAgentID: "root", messageIDs: map[string]string{}, requestedTools: map[multiAgentToolKey]multiAgentToolRequested{}, pendingTools: map[string]multiAgentToolCorrelation{}, toolIDs: map[multiAgentToolKey]string{}}
	b := &MultiAgentClient{}
	var messages []Message
	var prompt string
	emit := func(r AssistantResponse) error {
		if r.Data.Attributes.Prompt != "" {
			prompt = r.Data.Attributes.Prompt
		} else {
			messages = append(messages, r.Data.Attributes.StructuredMessage)
		}
		return nil
	}
	sequence := 0
	event := func(agent, typ, payload string) {
		t.Helper()
		sequence++
		_, err := b.handleEvent(run, multiAgentEnvelope{AgentID: agent, Sequence: sequence, Type: typ, Event: json.RawMessage(payload)}, emit)
		if err != nil {
			t.Fatal(err)
		}
	}
	event("root", "thinking.started", `{}`)
	event("root", "thinking.fragment_received", `{"fragment":"Reasoning"}`)
	event("root", "thinking.completed", `{"full_thinking":"Reasoning","redacted":false}`)
	event("child", "thinking.completed", `{"full_thinking":"private"}`)
	event("root", "debug.context", `{"content":"debug prompt"}`)
	event("root", "tool.call_started", `{"tool_call_id":"client","tool_name":"read_file","is_client_side":true}`)
	event("root", "tool.call_input_delta", `{"tool_call_id":"client","partial_json":"{\"path\":"}`)
	event("root", "tool.call_requested", `{"tool_call_id":"client","tool_name":"read_file","tool_input":"{\"path\":\"file.txt\"}","is_client_side":true}`)
	event("root", "client_tool.batch_ready", `{"generation":7,"tool_call_ids":["client"]}`)
	event("root", "tool.call_started", `{"tool_call_id":"server","tool_name":"search","is_client_side":false}`)
	event("root", "tool.call_requested", `{"tool_call_id":"server","tool_name":"search","tool_input":"{\"query\":\"logs\"}","is_client_side":false}`)
	event("root", "model.response_completed", `{"input_tokens":11,"output_tokens":5,"context_window":128000}`)
	event("child", "model.response_completed", `{"input_tokens":2000,"output_tokens":100,"context_window":128000}`)
	var reasoning string
	var ids []string
	serverCalls := 0
	for _, m := range messages {
		switch m.Content.Type {
		case ContentThinking:
			reasoning += m.Content.TextBody()
		case ContentToolCallStarted:
			if m.Content.Tool.IsClientSide {
				ids = append(ids, m.Content.Tool.ToolCallID)
			}
		case ContentToolCallInputDelta:
			ids = append(ids, m.Content.Tool.ToolCallID)
		case ContentClientToolCall:
			ids = append(ids, m.Content.Tool.ToolCallID)
			if m.Content.Tool.Metadata.Input != `{"path":"file.txt"}` {
				t.Fatal("final input was not authoritative")
			}
		case ContentToolCall:
			serverCalls++
			if m.Content.Tool.Metadata.Input != `{"query":"logs"}` {
				t.Fatal("server arguments missing")
			}
		}
	}
	if reasoning != "Reasoning" || prompt != "debug prompt" || serverCalls != 1 {
		t.Fatalf("reasoning=%q prompt=%q serverCalls=%d", reasoning, prompt, serverCalls)
	}
	if len(ids) != 3 || ids[0] != ids[1] || ids[0] != ids[2] {
		t.Fatalf("preview/final IDs = %v", ids)
	}
	if usage := messages[len(messages)-1].Results.Usage; usage.MaxTokens != 128000 || usage.TokensUsed != 16 {
		t.Fatalf("usage=%+v", usage)
	}
	if run.pendingTools[ids[0]].generation != 7 {
		t.Fatal("server batch generation lost")
	}
}

func TestMultiAgentTransportEligibility(t *testing.T) {
	b := &MultiAgentClient{}
	for _, test := range []struct {
		name, prompt string
		opts         SendOptions
		want         bool
	}{
		{name: "normal", prompt: "hello", want: true},
		{name: "reasoning", prompt: "hello", opts: SendOptions{InferenceMode: "deep"}},
		{name: "long prompt", prompt: strings.Repeat("a", 32001)},
		{name: "long instructions", prompt: "hello", opts: SendOptions{CustomUserContext: strings.Repeat("a", 32001)}},
		{name: "large image", prompt: "describe", opts: SendOptions{Context: &AssistantContext{Entities: []ContextEntity{ImageEntity("image/png", make([]byte, 128000), "image")}}}},
		{name: "large tool schema", prompt: "question", opts: SendOptions{ClientTools: []ClientTool{{Name: "tool", InputSchema: map[string]any{"description": strings.Repeat("a", 64001)}}}}},
		{name: "too many tools", prompt: "question", opts: SendOptions{ClientTools: make([]ClientTool, 65)}},
		{name: "too many overrides", prompt: "question", opts: SendOptions{SkillOverrides: make([]SkillOverride, 65)}},
		{name: "unicode prompt", prompt: strings.Repeat("é", 32000), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := b.CanStream(test.prompt, test.opts); got != test.want {
				t.Fatalf("CanStream=%v, want %v", got, test.want)
			}
		})
	}
}

func TestMultiAgentServerToolApprovalCorrelation(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started_%t", started), func(t *testing.T) {
			run := &multiAgentRun{
				id: multiAgentRunID, messageIDs: map[string]string{},
				requestedTools: map[multiAgentToolKey]multiAgentToolRequested{},
				pendingTools:   map[string]multiAgentToolCorrelation{}, toolIDs: map[multiAgentToolKey]string{},
			}
			backend := &MultiAgentClient{}
			var ids []string
			sequence := 0
			event := func(typ, payload string) {
				t.Helper()
				sequence++
				_, err := backend.handleEvent(run, multiAgentEnvelope{AgentID: "root", Sequence: sequence, Type: typ, Event: json.RawMessage(payload)}, func(response AssistantResponse) error {
					if tool := response.Data.Attributes.StructuredMessage.Content.Tool; tool != nil {
						ids = append(ids, tool.ToolCallID)
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			event("model.response_started", `{}`)
			if started {
				event("tool.call_started", `{"tool_call_id":"write","tool_name":"server_write","is_client_side":false}`)
			}
			event("tool.call_requested", `{"tool_call_id":"write","tool_name":"server_write","tool_input":"{}","is_client_side":false}`)
			event("tool.call_requested", `{"tool_call_id":"write","tool_name":"approval_request","tool_input":"{\"tool_call_id\":\"write\",\"tool_name\":\"server_write\",\"tool_args\":{}}","is_client_side":true}`)
			event("client_tool.batch_ready", `{"generation":1,"tool_call_ids":["write"]}`)
			event("tool.call_completed", `{"tool_call_id":"write","tool_name":"server_write","status":"success"}`)
			for _, id := range ids {
				if id != ids[0] {
					t.Fatalf("server call, approval and completion must share one local identity: %v", ids)
				}
			}
			if correlation := run.pendingTools[ids[0]]; correlation.wireToolCallID != "write" || correlation.generation != 1 {
				t.Fatalf("approval correlation = %+v", correlation)
			}
			originalID := ids[0]
			event("tool.call_requested", `{"tool_call_id":"write","tool_name":"approval_request","tool_input":"{\"tool_call_id\":\"write\",\"tool_name\":\"server_write\",\"tool_args\":{}}","is_client_side":true}`)
			event("client_tool.batch_ready", `{"generation":2,"tool_call_ids":["write"]}`)
			if ids[len(ids)-1] == originalID {
				t.Fatalf("new approval generation reused resolved ID %q", originalID)
			}
		})
	}
}

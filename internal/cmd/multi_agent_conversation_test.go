package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/headless"
)

func TestMultiAgentEngineConversationLifecycle(t *testing.T) {
	const conversationID = "12345678-1234-1234-1234-123456789abc"
	const newConversationID = "52345678-1234-1234-1234-123456789abc"
	const runsPath = "/api/unstable/assistant/multi-agent/runs"
	const continuePath = "/api/unstable/assistant/multi-agent/conversation/" + conversationID
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		if request == 3 {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v2/assistant/conversation-history/"+conversationID {
				t.Errorf("restore request = %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"data":{"type":"conversation-history-response","id":"history-response-id","attributes":{
				"title":"First question","owner_user_uuid":"owner","shared_expires_at":null,
				"messages":[
					{"role":"user","message_id":"user-1","agent_id":"command","content":{"type":"markdown_fragment","content":"First question"}},
					{"role":"assistant","message_id":"answer-1","agent_id":"command","content":{"type":"markdown_fragment","content":"First answer"}},
					{"role":"user","message_id":"user-2","agent_id":"command","content":{"type":"markdown_fragment","content":"Follow-up question"}},
					{"role":"assistant","message_id":"answer-2","agent_id":"command","content":{"type":"markdown_fragment","content":"Follow-up answer"}}
				]
			}}}`)
			return
		}
		var path, runID, id, prompt, answer string
		switch request {
		case 1:
			path, runID, id, prompt, answer = runsPath, conversationID, conversationID, "First question", "First answer"
		case 2:
			path, runID, id, prompt, answer = continuePath, "22345678-1234-1234-1234-123456789abc", conversationID, "Follow-up question", "Follow-up answer"
		case 4:
			path, runID, id, prompt, answer = continuePath, "42345678-1234-1234-1234-123456789abc", conversationID, "Resumed question", "Resumed answer"
		case 5:
			path, runID, id, prompt, answer = runsPath, newConversationID, newConversationID, "New question", "New answer"
		default:
			t.Errorf("unexpected request %d: %s %s", request, r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != path {
			t.Errorf("request %d = %s %s, want POST %s", request, r.Method, r.URL.Path, path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			Prompt  string `json:"prompt"`
			Profile string `json:"profile"`
			Model   string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Prompt != prompt || body.Profile != "cli" || body.Model != "selected-model" {
			t.Errorf("request %d payload = %+v", request, body)
		}
		w.Header().Set("X-Assistant-Conversation-ID", id)
		w.Header().Set("X-Assistant-Multi-Agent-Run-ID", runID)
		w.Header().Set("X-Assistant-Multi-Agent-Root-Agent-ID", "root")
		w.Header().Set("Content-Type", "text/event-stream")
		for i, item := range []struct {
			typ   string
			event any
		}{
			{"model.response_started", map[string]any{"model_provider": "openai", "model_name": "selected-model"}},
			{"content.fragment_received", map[string]any{"fragment": answer}},
			{"run.terminal", map[string]any{"outcome": "completed", "code": "completed", "message": "Run completed."}},
		} {
			raw, err := json.Marshal(item.event)
			if err != nil {
				t.Error(err)
				return
			}
			_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":%q,\"run_id\":%q,\"agent_id\":\"root\",\"sequence\":%d,\"type\":%q,\"event\":%s}\n\n", id, runID, i+1, item.typ, raw)
		}
	}))
	defer server.Close()
	newEngine := func(id string) *agent.Engine {
		backend := assistant.NewMultiAgentClient(&assistant.Client{BaseURL: server.URL, HTTPClient: server.Client()})
		return agent.New(backend, agent.Config{ConversationID: id, Model: "selected-model"})
	}
	tools, err := agent.NewToolSet(agent.ModeSkipPermissions)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	turn := func(engine *agent.Engine, prompt, id string) headless.Finish {
		t.Helper()
		delivery := &multiAgentRecordingDelivery{}
		outcome, err := headless.Run(ctx, engine, agent.TurnInput{Message: prompt, Tools: tools}, delivery)
		if err != nil {
			t.Fatal(err)
		}
		result := delivery.finish
		if outcome != headless.OutcomeCompleted || result.Outcome != headless.OutcomeCompleted || result.ConversationID != id || engine.ConversationID() != id {
			t.Fatalf("turn result = %+v, engine conversation = %q, want completed %q", result, engine.ConversationID(), id)
		}
		return result
	}
	assertTranscript := func(blocks []agent.Block, want ...string) {
		t.Helper()
		if len(blocks) != len(want) {
			t.Fatalf("transcript has %d blocks, want %d: %+v", len(blocks), len(want), blocks)
		}
		for i, text := range want {
			role := assistant.RoleUser
			if i%2 == 1 {
				role = assistant.RoleAssistant
			}
			if blocks[i].Role != role || blocks[i].Markdown == nil || blocks[i].Markdown.Content != text || !blocks[i].Complete {
				t.Errorf("block %d = %+v, want complete %v %q", i, blocks[i], role, text)
			}
		}
	}
	engine := newEngine("")
	first := turn(engine, "First question", conversationID)
	assertTranscript(first.Blocks, "First question", "First answer")
	followup := turn(engine, "Follow-up question", conversationID)
	assertTranscript(followup.Blocks, "First question", "First answer", "Follow-up question", "Follow-up answer")

	resumed := newEngine(conversationID)
	var restored []agent.Block
	for event := range resumed.Restore(ctx) {
		if event.Kind == agent.EventError {
			t.Fatal(event.Err)
		}
		if event.Kind == agent.EventTranscript {
			restored = event.Transcript.Blocks
			if event.Origin != agent.TranscriptOriginRestore {
				t.Errorf("restored transcript origin = %v", event.Origin)
			}
		}
	}
	assertTranscript(restored, "First question", "First answer", "Follow-up question", "Follow-up answer")
	continued := turn(resumed, "Resumed question", conversationID)
	assertTranscript(continued.Blocks, "First question", "First answer", "Follow-up question", "Follow-up answer", "Resumed question", "Resumed answer")

	if err := resumed.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if resumed.ConversationID() != "" || resumed.PreviousConversationID() != conversationID {
		t.Fatalf("reset conversation = %q, previous = %q", resumed.ConversationID(), resumed.PreviousConversationID())
	}
	newTurn := turn(resumed, "New question", newConversationID)
	assertTranscript(newTurn.Blocks, "New question", "New answer")
	if requests.Load() != 5 {
		t.Fatalf("requests = %d, want four turns and one history read", requests.Load())
	}
}

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMultiAgentConversationContinuesWithFreshRunIdentity(t *testing.T) {
	const conversationID = "12345678-1234-1234-1234-123456789abc"
	const nextRunID = "22345678-1234-1234-1234-123456789abc"
	turn := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		turn++
		path, runID := multiAgentRunsPath, conversationID
		if turn == 2 {
			path, runID = "/api/unstable/assistant/multi-agent/conversation/"+conversationID, nextRunID
		}
		if r.URL.Path != path || r.Method != http.MethodPost {
			t.Errorf("turn %d request = %s %s, want POST %s", turn, r.Method, r.URL.Path, path)
		}
		var body struct {
			Prompt  string `json:"prompt"`
			Profile string `json:"profile"`
			Model   string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Profile != "cli" || body.Model != "selected-model" || body.Prompt != fmt.Sprintf("turn %d", turn) {
			t.Errorf("turn payload = %+v", body)
		}
		w.Header().Set("X-Assistant-Conversation-ID", conversationID)
		w.Header().Set(multiAgentRunIDHeader, runID)
		w.Header().Set(multiAgentRootIDHeader, "root")
		_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":%q,\"run_id\":%q,\"sequence\":1,\"type\":\"run.terminal\",\"event\":{\"outcome\":\"completed\"}}\n\n", conversationID, runID)
	})
	backend := NewMultiAgentClient(c)
	conversation := ""
	for i := 1; i <= 2; i++ {
		err := backend.Stream(context.Background(), fmt.Sprintf("turn %d", i), SendOptions{ConversationID: conversation, Model: "selected-model"}, func(response AssistantResponse) error {
			conversation = response.Data.Attributes.ConversationID
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if conversation != conversationID {
			t.Fatalf("conversation = %q, want %q even without content events", conversation, conversationID)
		}
	}
}

func TestMultiAgentConversationRetainsIdentityBeforeStreamFailure(t *testing.T) {
	const conversationID = "12345678-1234-1234-1234-123456789abc"
	const runID = "22345678-1234-1234-1234-123456789abc"
	var cancelPath string
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancelPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("X-Assistant-Conversation-ID", conversationID)
		w.Header().Set(multiAgentRunIDHeader, runID)
		w.Header().Set(multiAgentRootIDHeader, "root")
		_, _ = fmt.Fprint(w, "data: invalid-json\n\n")
	})
	gotID := ""
	err := NewMultiAgentClient(c).Stream(context.Background(), "continue", SendOptions{ConversationID: conversationID}, func(response AssistantResponse) error {
		gotID = response.Data.Attributes.ConversationID
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "decode multi-agent SSE") {
		t.Fatalf("stream error = %v", err)
	}
	if gotID != conversationID {
		t.Fatalf("lost conversation on stream failure: %q", gotID)
	}
	if cancelPath != multiAgentRunsPath+"/"+runID+"/cancel" {
		t.Fatalf("cancel path = %q", cancelPath)
	}
}

func TestMultiAgentConversationExposesHistoryBackend(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/assistant/conversation-history/"+multiAgentRunID {
			t.Errorf("history request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"data":{"attributes":{"messages":[]}}}`)
	})
	backend, ok := any(NewMultiAgentClient(c)).(interface {
		ConversationHistory(context.Context, ConversationHistoryInput) (*ConversationHistoryResponse, error)
	})
	if !ok {
		t.Fatal("multi-agent backend has no standard history capability")
	}
	_, err := backend.ConversationHistory(context.Background(), ConversationHistoryInput{ConversationID: multiAgentRunID})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMultiAgentConversationRejectsMismatchedEventIdentity(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("X-Assistant-Conversation-ID", multiAgentRunID)
		w.Header().Set(multiAgentRunIDHeader, multiAgentRunID)
		w.Header().Set(multiAgentRootIDHeader, "root")
		_, _ = fmt.Fprintf(w, "data: {\"conversation_id\":\"other-conversation\",\"run_id\":%q,\"sequence\":1,\"type\":\"content.fragment_received\",\"agent_id\":\"root\",\"event\":{\"fragment\":\"wrong history\"}}\n\n", multiAgentRunID)
	})
	var text string
	err := NewMultiAgentClient(c).Stream(context.Background(), "investigate", SendOptions{}, func(response AssistantResponse) error {
		text += response.Data.Attributes.StructuredMessage.Content.TextBody()
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "event conversation ID") {
		t.Fatalf("identity mismatch error = %v", err)
	}
	if text != "" {
		t.Fatalf("accepted foreign conversation content: %q", text)
	}
}

func TestMultiAgentConversationRejectsUnsupportedOverrides(t *testing.T) {
	for _, opts := range []SendOptions{
		{MessageHistory: []json.RawMessage{json.RawMessage(`{}`)}},
	} {
		c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
			t.Error("unsupported option was silently sent to server")
			w.WriteHeader(http.StatusBadRequest)
		})
		err := NewMultiAgentClient(c).Stream(context.Background(), "investigate", opts, nil)
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("unsupported override error = %v", err)
		}
	}
}

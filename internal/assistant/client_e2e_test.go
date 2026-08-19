package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests hit the real Bits assistant API. They are opt-in: set
// BITS_ASSISTANT_E2E=1 and provide DD_API_KEY / DD_APP_KEY (as populated by
// `dd-auth --domain dd.datad0g.com`). Without the flag they skip, so `go test`
// stays hermetic by default.
//
// The assistant is an LLM, so response *content* is not deterministic. The
// assertions therefore split into two kinds:
//
//   - Structural: a turn returns a conversation id and at least one non-empty
//     markdown fragment, and every streamed line decodes cleanly into our
//     types. This exercises request encoding and response decoding end to end.
//   - Deterministic: rename, share/unshare, list, history, and delete have
//     exact, model-independent outcomes we assert precisely.
//
// The client-tool loop test is soft on whether the model actually calls the
// tool (that is model-dependent); it asserts the protocol round-trips without
// error and, when the tool is called, that input/output flow back correctly.

func requireE2E(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("BITS_ASSISTANT_E2E") == "" {
		t.Skip("set BITS_ASSISTANT_E2E=1 (with DD_API_KEY/DD_APP_KEY) to run assistant e2e tests")
	}
	c, err := NewClient()
	if err != nil {
		t.Fatalf("NewClient: %v (BITS_ASSISTANT_E2E is set but credentials are missing)", err)
	}
	return c
}

func TestE2E_ConversationLifecycle(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Structural: send a message and collect assistant text.
	var text strings.Builder
	convID, err := c.Send(ctx, "Reply with a short one-sentence greeting.", SendOptions{}, func(ar AssistantResponse) error {
		if ct := ar.Data.Attributes.StructuredMessage.Content; ct.Kind() == KindText {
			text.WriteString(ct.TextBody())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if convID == "" {
		t.Fatal("Send returned an empty conversation id")
	}
	// Best-effort cleanup even if a later assertion fails.
	defer func() {
		_ = c.DeleteConversation(context.Background(), DeleteConversationInput{ConversationID: convID})
	}()
	if strings.TrimSpace(text.String()) == "" {
		t.Fatalf("expected non-empty assistant text, got %q", text.String())
	}

	// Structural: history returns the turn we just had.
	hist, err := c.ConversationHistory(ctx, ConversationHistoryInput{ConversationID: convID})
	if err != nil {
		t.Fatalf("ConversationHistory: %v", err)
	}
	if len(hist.Data.Attributes.Messages) == 0 {
		t.Fatal("conversation history has no messages")
	}
	if hist.Data.Attributes.OwnerUserUUID == "" {
		t.Error("expected owner_user_uuid to be populated in history")
	}

	// Deterministic: rename and read the title back.
	title := fmt.Sprintf("bits-e2e-%d", time.Now().UnixNano())
	if err := c.RenameConversation(ctx, RenameConversationInput{ConversationID: convID, Title: title}); err != nil {
		t.Fatalf("RenameConversation: %v", err)
	}
	renamed, err := c.ConversationHistory(ctx, ConversationHistoryInput{ConversationID: convID})
	if err != nil {
		t.Fatalf("ConversationHistory after rename: %v", err)
	}
	if got := renamed.Data.Attributes.Title; got != title {
		t.Errorf("title after rename = %q, want %q", got, title)
	}

	// Deterministic: the conversation appears in the user's list.
	convs, err := c.UserConversations(ctx)
	if err != nil {
		t.Fatalf("UserConversations: %v", err)
	}
	found := false
	for _, s := range convs.Data.Attributes.Conversations {
		if s.ConversationID == convID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("conversation %s not present in user conversations", convID)
	}

	// Deterministic: sharing sets an expiry, unsharing clears it.
	ttl := 7
	shared, err := c.ShareConversation(ctx, ShareConversationInput{ConversationID: convID, Shared: true, TTLDays: &ttl})
	if err != nil {
		t.Fatalf("ShareConversation(true): %v", err)
	}
	if shared.SharedExpires == nil {
		t.Error("expected shared_expires_at to be set after sharing")
	}
	unshared, err := c.ShareConversation(ctx, ShareConversationInput{ConversationID: convID, Shared: false})
	if err != nil {
		t.Fatalf("ShareConversation(false): %v", err)
	}
	if unshared.SharedExpires != nil {
		t.Errorf("expected shared_expires_at to be nil after unsharing, got %d", *unshared.SharedExpires)
	}

	// Deterministic: after deletion, fetching history fails.
	if err := c.DeleteConversation(ctx, DeleteConversationInput{ConversationID: convID}); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := c.ConversationHistory(ctx, ConversationHistoryInput{ConversationID: convID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound fetching history for a deleted conversation, got %v", err)
	}
}

func TestE2E_ListSkills(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The set of skills is org-dependent; we only assert the call decodes.
	if _, err := c.ListSkills(ctx); err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
}

func TestE2E_ExperimentalToolFlags(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := c.ExperimentalToolFlags(ctx); err != nil {
		t.Fatalf("ExperimentalToolFlags: %v", err)
	}
}

func TestE2E_RunToolsClientTool(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	called := false
	var gotInput string
	tools := []Tool{{
		ClientTool: ClientTool{
			Name:        "get_secret_word",
			Description: "Returns the secret word. Call this whenever the user asks for the secret word.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Run: func(_ context.Context, input string) (string, error) {
			called = true
			gotInput = input
			return `{"word":"bananaphone"}`, nil
		},
	}}

	convID, err := c.RunTools(ctx, "Call the get_secret_word tool, then tell me the secret word.", tools, SendOptions{}, nil)
	if err != nil {
		t.Fatalf("RunTools: %v", err)
	}
	if convID == "" {
		t.Fatal("RunTools returned an empty conversation id")
	}
	defer func() {
		_ = c.DeleteConversation(context.Background(), DeleteConversationInput{ConversationID: convID})
	}()

	if called {
		// input is a JSON string (possibly "{}"); it must at least be present.
		if gotInput == "" {
			t.Error("client tool was called with empty input")
		}
	} else {
		t.Log("model did not invoke the client tool (model-dependent); protocol still exercised without error")
	}
}

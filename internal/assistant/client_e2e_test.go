package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/auth"
)

// These tests hit the real Bits assistant API. They are opt-in: either set
// BITS_ASSISTANT_E2E=1 and provide DD_API_KEY / DD_APP_KEY (as populated by
// `dd-auth --domain dd.datad0g.com`), or set BITS_OAUTH_E2E=1 after `bits login`.
// Without either flag they skip, so `go test` stays hermetic by default.
//
// The assistant is an LLM, so response *content* is not deterministic. The
// assertions therefore split into two kinds:
//
//   - Structural: a turn returns a conversation id and at least one non-empty
//     markdown fragment, and every streamed line decodes cleanly into our
//     types. This exercises request encoding and response decoding end to end.
//   - Deterministic: rename, share/unshare, list, history, and delete have
//     exact, model-independent outcomes we assert precisely.

func requireE2E(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("BITS_OAUTH_E2E") != "" {
		store := auth.DefaultStore()
		session, err := store.Load()
		if err != nil {
			t.Fatalf("load OAuth session: %v (run bits login first)", err)
		}
		source, err := auth.NewSource(session, store, nil)
		if err != nil {
			t.Fatalf("OAuth token source: %v", err)
		}
		client, err := NewOAuthClient(source.Site(), source)
		if err != nil {
			t.Fatalf("NewOAuthClient: %v", err)
		}
		return client
	}
	if os.Getenv("BITS_ASSISTANT_E2E") == "" {
		t.Skip("set BITS_OAUTH_E2E=1 after bits login, or BITS_ASSISTANT_E2E=1 with DD_API_KEY/DD_APP_KEY")
	}
	client, err := NewAPIKeyClient(
		os.Getenv("DD_SITE_URL"),
		os.Getenv("DD_API_KEY"),
		os.Getenv("DD_APP_KEY"),
	)
	if err != nil {
		t.Fatalf("NewAPIKeyClient: %v (BITS_ASSISTANT_E2E is set but credentials are missing)", err)
	}
	return client
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

// TestE2E_ResumeConversationLifecycle covers the cross-surface contract used by
// /resume: create a disposable conversation, find it through the same list API
// used by Bits web, load its history, append using that exact id, and verify the
// persisted user turns remain ordered and unique.
func TestE2E_ResumeConversationLifecycle(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nonce := time.Now().UnixNano()
	first := fmt.Sprintf("bits-resume-e2e-first-%d", nonce)
	second := fmt.Sprintf("bits-resume-e2e-second-%d", nonce)
	convID, err := c.Send(ctx, first, SendOptions{}, nil)
	if err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if convID == "" {
		t.Fatal("initial Send returned an empty conversation id")
	}
	defer func() {
		_ = c.DeleteConversation(context.Background(), DeleteConversationInput{ConversationID: convID})
	}()

	listed, err := c.UserConversations(ctx)
	if err != nil {
		t.Fatalf("UserConversations: %v", err)
	}
	if listed.Data.Type != "user-conversations-response" {
		t.Fatalf("list response type = %q", listed.Data.Type)
	}
	found := false
	for _, summary := range listed.Data.Attributes.Conversations {
		if summary.ConversationID != convID {
			continue
		}
		found = true
		if summary.ID != convID {
			t.Errorf("JSON:API id = %q, want canonical conversation_id %q", summary.ID, convID)
		}
		if summary.UpdatedAt <= 0 {
			t.Errorf("updated_at = %d, want milliseconds timestamp", summary.UpdatedAt)
		}
		break
	}
	if !found {
		t.Fatalf("conversation %s not present in user list", convID)
	}

	before, err := c.ConversationHistory(ctx, ConversationHistoryInput{ConversationID: convID})
	if err != nil {
		t.Fatalf("ConversationHistory before append: %v", err)
	}
	if before.Data.Type != "conversation-history-response" {
		t.Fatalf("history response type = %q", before.Data.Type)
	}
	if before.Data.ID == convID {
		t.Errorf("history data.id unexpectedly equals conversation id %q; contract says it identifies the response", convID)
	}

	appendedID, err := c.Send(ctx, second, SendOptions{ConversationID: convID}, nil)
	if err != nil {
		t.Fatalf("append Send: %v", err)
	}
	if appendedID != convID {
		t.Fatalf("append returned conversation id %q, want %q", appendedID, convID)
	}
	after, err := c.ConversationHistory(ctx, ConversationHistoryInput{ConversationID: convID})
	if err != nil {
		t.Fatalf("ConversationHistory after append: %v", err)
	}

	positions := map[string][]int{first: nil, second: nil}
	for i, message := range after.Data.Attributes.Messages {
		if message.Role != "user" || message.Content.Kind() != KindText || message.Content.Markdown == nil {
			continue
		}
		if _, ok := positions[message.Content.Markdown.Content]; ok {
			positions[message.Content.Markdown.Content] = append(positions[message.Content.Markdown.Content], i)
		}
	}
	if len(positions[first]) != 1 || len(positions[second]) != 1 {
		t.Fatalf("persisted user turn positions = %#v, want each exactly once", positions)
	}
	if positions[first][0] >= positions[second][0] {
		t.Fatalf("persisted user turns out of order: first=%d second=%d", positions[first][0], positions[second][0])
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

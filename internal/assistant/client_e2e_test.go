package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/auth"
)

// These tests hit the real Bits assistant API. They are opt-in: either set
// BITS_OAUTH_E2E=1 after `bits login`, or set BITS_ASSISTANT_E2E=1 with
// DD_API_KEY / DD_APP_KEY (a Datadog API key pair) plus BITS_E2E_SITE naming
// the site to use. Without either flag they skip, so `go test` stays hermetic
// by default and no run ever falls back to an implicit site.
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
		t.Skip("set BITS_OAUTH_E2E=1 after bits login, or BITS_ASSISTANT_E2E=1 with DD_API_KEY/DD_APP_KEY and BITS_E2E_SITE")
	}
	e2eSite := strings.TrimSpace(os.Getenv("BITS_E2E_SITE"))
	if e2eSite == "" {
		t.Fatal("BITS_ASSISTANT_E2E=1 requires BITS_E2E_SITE naming the API site to use")
	}
	client, err := NewAPIKeyClient(
		e2eSite,
		os.Getenv("DD_API_KEY"),
		os.Getenv("DD_APP_KEY"),
	)
	if err != nil {
		t.Fatalf("NewAPIKeyClient: %v (BITS_ASSISTANT_E2E is set but credentials are missing)", err)
	}
	return client
}

func TestE2E_CurrentUser(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user, err := c.CurrentUser(ctx)
	if err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if strings.TrimSpace(user.Name) == "" && strings.TrimSpace(user.Handle) == "" && strings.TrimSpace(user.Email) == "" {
		t.Fatal("CurrentUser returned no user display field")
	}
	if strings.TrimSpace(user.Organization) == "" && strings.TrimSpace(user.OrganizationID) == "" {
		t.Fatal("CurrentUser returned no organization display field")
	}
}

func TestE2E_SearchEntitiesReturnsMultipleTypes(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := c.SearchEntities(ctx, SearchEntitiesInput{
		SearchSessionID: "8fdc4cb4-1f3f-4b78-bd4c-8f50c3b8d9a1",
		RawQuery:        "a",
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("SearchEntities: %v", err)
	}
	types := make(map[EntityType]struct{})
	attached := make([]ContextEntity, 0, 2)
	for _, entity := range response.Entities {
		if entity.EntityID == "" || entity.EntityType == "" {
			t.Fatalf("entity lacks canonical identity: %#v", entity)
		}
		if _, seen := types[entity.EntityType]; !seen && len(attached) < 2 {
			attached = append(attached, ContextEntity{
				Type: entity.EntityType, ID: entity.EntityID, Label: entity.DisplayLabel(),
			})
		}
		types[entity.EntityType] = struct{}{}
	}
	if len(types) < 2 {
		t.Fatalf("entity search returned %d distinct types: %#v", len(types), response.Entities)
	}
	conversationID, err := c.Send(ctx, "Name the two attached Datadog entities.", SendOptions{
		Context: &AssistantContext{Entities: attached},
	}, nil)
	if err != nil {
		t.Fatalf("attach two entity types: %v", err)
	}
	if conversationID == "" {
		t.Fatal("attachment verification returned no conversation id")
	}
	defer func() {
		_ = c.DeleteConversation(context.Background(), DeleteConversationInput{ConversationID: conversationID})
	}()
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

// TestE2E_ClientToolOutOfBandContent proves the backend stores a client tool
// response's display Content out of the model's token band, separate from the
// model-visible Metadata.Output, and that the display content is retrievable via
// the history API.
//
// The wire contract (types.go): ClientToolResponse.Content is a display-only
// markdown block, while ClientToolResponse.Metadata.Output is what the model
// sees. On the history side these surface on the tool payload as
// Content.Tool.Detail (display markdown) and Content.Tool.Metadata.Output
// (model output). We reply with two distinct nonces so we can tell which field
// each round-tripped into.
func TestE2E_ClientToolOutOfBandContent(t *testing.T) {
	c := requireE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	nonce := time.Now().UnixNano()
	displayNonce := fmt.Sprintf("display-only-%d", nonce)
	outputNonce := fmt.Sprintf("model-output-%d", nonce)

	tool := ClientTool{
		Name:        "record_note",
		Description: "Record a short note. Call this exactly once with the given text.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"note": map[string]any{"type": "string"},
			},
			"required": []string{"note"},
		},
	}

	prompt := "Call the record_note tool exactly once, with the note argument set to " +
		"the exact string \"hello from e2e\". Do not write any other text and do not " +
		"call any other tool. Just make that single tool call."

	var (
		toolCallID string
		toolInput  string
	)
	convID, err := c.Send(ctx, prompt, SendOptions{ClientTools: []ClientTool{tool}}, func(ar AssistantResponse) error {
		ct := ar.Data.Attributes.StructuredMessage.Content
		// A client-side tool invocation surfaces as ContentClientToolCall; the
		// stream pauses until we answer it. This is the same discriminator the
		// engine uses (agent/engine.go).
		if ct.Type == ContentClientToolCall && ct.Tool != nil && toolCallID == "" {
			toolCallID = ct.Tool.ToolCallID
			if ct.Tool.Metadata != nil {
				toolInput = ct.Tool.Metadata.Input
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if convID == "" {
		t.Fatal("initial Send returned an empty conversation id")
	}
	defer func() {
		_ = c.DeleteConversation(context.Background(), DeleteConversationInput{ConversationID: convID})
	}()
	if toolCallID == "" {
		t.Fatalf("model did not call the record_note client tool; cannot exercise out-of-band content contract")
	}

	// Answer the client tool call: distinct display Content vs model Output.
	responses := []ClientToolResponse{{
		Type:       "client_tool_response",
		ToolCallID: toolCallID,
		Title:      "record_note",
		Status:     ToolStatusSuccess,
		Content:    &MarkdownContent{Type: ContentMarkdownFragment, Content: displayNonce},
		Metadata: ClientToolMetadata{
			Name:   "record_note",
			Input:  toolInput,
			Output: outputNonce,
		},
	}}
	// Send the follow-up on the same conversation and drain its continuation.
	// The model may reply with text or (defensively) call again; we don't need
	// to answer a second call, since the response we assert on is the one we
	// just persisted above.
	if _, err := c.Send(ctx, responses, SendOptions{ConversationID: convID, ClientTools: []ClientTool{tool}}, func(AssistantResponse) error {
		return nil
	}); err != nil {
		t.Fatalf("follow-up Send with ClientToolResponse: %v", err)
	}

	hist, err := c.ConversationHistory(ctx, ConversationHistoryInput{ConversationID: convID})
	if err != nil {
		t.Fatalf("ConversationHistory: %v", err)
	}

	var (
		found      bool
		gotDisplay string
		gotOutput  string
	)
	for _, m := range hist.Data.Attributes.Messages {
		ct := m.Content
		if ct.Kind() != KindToolResult || ct.Tool == nil {
			continue
		}
		// Match on the tool_call_id we answered, falling back to the message
		// whose model output equals our output nonce.
		matches := ct.Tool.ToolCallID == toolCallID
		if !matches && ct.Tool.Metadata != nil && ct.Tool.Metadata.Output == outputNonce {
			matches = true
		}
		if !matches {
			continue
		}
		found = true
		if ct.Tool.Detail != nil {
			gotDisplay = ct.Tool.Detail.Content
		}
		if ct.Tool.Metadata != nil {
			gotOutput = ct.Tool.Metadata.Output
		}
		break
	}
	if !found {
		t.Fatalf("no tool-response message for tool_call_id %q (or output nonce %q) in history", toolCallID, outputNonce)
	}

	// The model-visible output must round-trip into the metadata output field.
	if gotOutput != outputNonce {
		t.Errorf("metadata output = %q, want model-output nonce %q", gotOutput, outputNonce)
	}
	// The key finding: the display-only content must be preserved and
	// retrievable via the history display field (Content.Tool.Detail), NOT the
	// model output. If the history does not expose it at all, this fails
	// explicitly so the result is unambiguous.
	if gotDisplay != displayNonce {
		t.Errorf("display content on tool payload Detail = %q, want display-only nonce %q "+
			"(display content not preserved/retrievable out of band)", gotDisplay, displayNonce)
	}
	// The two fields must be genuinely distinct: the display nonce must not have
	// leaked into the model output, and vice versa.
	if gotOutput == displayNonce {
		t.Errorf("display-only nonce leaked into model output field: %q", gotOutput)
	}
	if gotDisplay == outputNonce {
		t.Errorf("model-output nonce appeared in display content field: %q", gotDisplay)
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

package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
)

// This opt-in test proves the engine reset contract against the real Assistant
// API: no explicit create call is needed, the next turn gets a distinct ID, and
// both persisted conversations remain independently loadable. Both disposable
// conversations are deleted during cleanup.
func TestE2E_NewConversationResetLifecycle(t *testing.T) {
	if os.Getenv("BITS_ASSISTANT_E2E") == "" {
		t.Skip("set BITS_ASSISTANT_E2E=1 (DD_API_KEY/DD_APP_KEY and BITS_E2E_SITE) to run assistant e2e tests")
	}
	e2eSite := strings.TrimSpace(os.Getenv("BITS_E2E_SITE"))
	if e2eSite == "" {
		t.Fatal("BITS_ASSISTANT_E2E=1 requires BITS_E2E_SITE naming the API site to use")
	}
	// Route e2e credentials through the same canonicalization as the CLI so a
	// configured staging login origin also targets its API host.
	normalizedSite, err := auth.NormalizeAPISite(e2eSite)
	if err != nil {
		t.Fatal(err)
	}
	client, err := assistant.NewAPIKeyClient(
		normalizedSite,
		os.Getenv("DD_API_KEY"),
		os.Getenv("DD_APP_KEY"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	e := New(client, assistant.SendOptions{})

	turnCtx, cancelTurn := context.WithCancel(ctx)
	firstTurn := e.StartTurn(turnCtx, TurnInput{Message: "Count from 1 to 200, with one number per line."})
	cancelledActiveTurn := false
	for event := range firstTurn {
		if !cancelledActiveTurn && stateHasAssistant(event) {
			cancelledActiveTurn = true
			cancelTurn()
		}
	}
	cancelTurn()
	if !cancelledActiveTurn {
		t.Fatal("first turn produced no assistant event to cancel")
	}
	firstID := e.ConversationID()
	if firstID == "" {
		t.Fatal("first turn returned an empty conversation id")
	}
	var secondID string
	defer func() {
		for _, id := range []string{firstID, secondID} {
			if id != "" {
				_ = client.DeleteConversation(context.Background(), assistant.DeleteConversationInput{ConversationID: id})
			}
		}
	}()

	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if e.ConversationID() != "" {
		t.Fatalf("conversation id after reset = %q", e.ConversationID())
	}
	if e.PreviousConversationID() != firstID {
		t.Fatalf("previous conversation id = %q, want %q", e.PreviousConversationID(), firstID)
	}
	_ = drain(e.StartTurn(ctx, TurnInput{Message: "Reply with exactly: second conversation"}))
	secondID = e.ConversationID()
	if secondID == "" || secondID == firstID {
		t.Fatalf("conversation ids before/after reset = %q/%q, want distinct non-empty ids", firstID, secondID)
	}
	for label, id := range map[string]string{"first": firstID, "second": secondID} {
		history, err := client.ConversationHistory(ctx, assistant.ConversationHistoryInput{ConversationID: id})
		if err != nil {
			t.Fatalf("load %s conversation %q: %v", label, id, err)
		}
		if len(history.Data.Attributes.Messages) == 0 {
			t.Fatalf("%s conversation %q has empty history", label, id)
		}
	}
	listed, err := client.UserConversations(ctx)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	found := map[string]bool{firstID: false, secondID: false}
	for _, conversation := range listed.Data.Attributes.Conversations {
		if _, tracked := found[conversation.ConversationID]; tracked {
			found[conversation.ConversationID] = true
		}
	}
	for id, present := range found {
		if !present {
			t.Errorf("conversation %q was not resumable through the user conversation list", id)
		}
	}
	for label, id := range map[string]string{"first": firstID, "second": secondID} {
		if err := client.DeleteConversation(ctx, assistant.DeleteConversationInput{ConversationID: id}); err != nil {
			t.Errorf("delete %s disposable conversation %q: %v", label, id, err)
		}
	}
}

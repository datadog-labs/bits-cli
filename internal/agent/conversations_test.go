package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
)

const testConversationID = "12345678-1234-1234-1234-123456781234"

// historyBackend also lists conversations and serves history: resp, unless
// history is set.
type historyBackend struct {
	scriptBackend
	list    *assistant.UserConversationsResponse
	resp    *assistant.ConversationHistoryResponse
	history func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error)
}

func (b *historyBackend) UserConversations(context.Context) (*assistant.UserConversationsResponse, error) {
	return b.list, nil
}

func (b *historyBackend) ConversationHistory(ctx context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	if b.history != nil {
		return b.history(ctx, in)
	}
	return b.resp, nil
}

func engineWithConversation(backend Backend, id string, messages ...assistant.Message) *Engine {
	e := New(backend, Config{ConversationID: id, Model: "retained"})
	for _, message := range messages {
		e.transcript.AppendMessage(message)
	}
	e.transcript.FinalizeAll()
	return e
}

func TestListConversationsCopiesAndHandlesFailures(t *testing.T) {
	response := &assistant.UserConversationsResponse{}
	response.Data.Type = "user-conversations-response"
	response.Data.Attributes.Conversations = []assistant.ConversationSummary{{ID: testConversationID, ConversationID: testConversationID, Title: "One"}}
	e := New(&historyBackend{list: response}, Config{})
	result := e.ListConversations(context.Background())
	if result.Err != nil || len(result.Conversations) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if e.OperationActive() {
		t.Fatal("list result published before releasing engine ownership")
	}
	response.Data.Attributes.Conversations[0].Title = "mutated"
	if result.Conversations[0].Title != "One" {
		t.Fatal("result aliases backend storage")
	}

	unsupportedEngine := New(&scriptBackend{}, Config{})
	unsupported := unsupportedEngine.ListConversations(context.Background())
	if !errors.Is(unsupported.Err, ErrConversationListUnsupported) {
		t.Fatalf("unsupported error = %v", unsupported.Err)
	}
	if unsupportedEngine.OperationActive() {
		t.Fatal("unsupported result retained engine ownership")
	}

	malformed := New(&historyBackend{}, Config{})
	if got := malformed.ListConversations(context.Background()).Err; !errors.Is(got, ErrMalformedConversationList) {
		t.Fatalf("nil response error = %v", got)
	}
	if malformed.OperationActive() {
		t.Fatal("malformed envelope retained engine ownership")
	}
	badIDs := &assistant.UserConversationsResponse{}
	badIDs.Data.Type = "user-conversations-response"
	badIDs.Data.Attributes.Conversations = []assistant.ConversationSummary{
		{ID: "response-item", ConversationID: "different"},
		{ID: testConversationID, ConversationID: testConversationID, Title: "valid"},
	}
	malformed = New(&historyBackend{list: badIDs}, Config{})
	result = malformed.ListConversations(context.Background())
	if result.Err != nil || result.Omitted != 1 || len(result.Conversations) != 1 || result.Conversations[0].ConversationID != testConversationID {
		t.Fatalf("partially malformed result = %+v", result)
	}
	if malformed.OperationActive() {
		t.Fatal("partial list result retained engine ownership")
	}
}

func TestLoadAndInstallConversation(t *testing.T) {
	response := &assistant.ConversationHistoryResponse{}
	response.Data.Type = "conversation-history-response"
	response.Data.ID = "response-id-is-not-conversation-id"
	response.Data.Attributes.Messages = []assistant.Message{
		assistant.AssistantMessage("m1", assistant.TextContent("Hello")),
		{Results: &assistant.Results{Usage: &assistant.Usage{TokensUsed: 42}}},
		assistant.AssistantMessage("m1", assistant.TextContent(" world")),
		assistant.AssistantMessage("turn", assistant.Content{Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: "ended"}}),
		assistant.AssistantMessage("stop", assistant.Content{Type: assistant.ContentUserStop, Stop: &assistant.StopPayload{Content: "stopped"}}),
		assistant.AssistantMessage("internal", assistant.Content{Type: assistant.ContentProviderCompaction, Compaction: &assistant.CompactionPayload{Summary: "private"}}),
		assistant.AssistantMessage("future", assistant.Content{Type: "future_content"}),
	}
	backend := &historyBackend{history: func(_ context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
		if in.ConversationID != testConversationID {
			t.Fatalf("history id = %q", in.ConversationID)
		}
		return response, nil
	}}
	e := engineWithConversation(backend, "old", assistant.AssistantMessage("old", assistant.TextContent("keep")))
	e.opts.MessageHistory = []json.RawMessage{json.RawMessage(`{"role":"user","content":"stale"}`)}
	before := e.Snapshot()
	conversation, err := e.LoadConversation(context.Background(), " "+testConversationID+" ")
	if err != nil {
		t.Fatal(err)
	}
	blocks := conversation.transcript.Blocks()
	if conversation.id != testConversationID || len(blocks) != 2 {
		t.Fatalf("loaded conversation = %+v", conversation)
	}
	if e.ConversationID() != "old" || !reflect.DeepEqual(e.Snapshot(), before) {
		t.Fatal("load mutated engine before installation")
	}
	if blocks[0].Markdown.Content != "Hello world" || blocks[1].Kind != assistant.KindUnknown {
		t.Fatalf("loaded blocks = %+v", blocks)
	}
	if e.OperationActive() {
		t.Fatal("loading claimed engine ownership")
	}
	if err := e.InstallConversation(context.Background(), conversation); err != nil {
		t.Fatal(err)
	}
	if e.OperationActive() {
		t.Fatal("install retained engine ownership")
	}
	if e.ConversationID() != testConversationID || !reflect.DeepEqual(e.Snapshot(), blocks) {
		t.Fatal("install did not replace the conversation")
	}
	history := e.opts.MessageHistory
	if history != nil {
		t.Fatalf("install retained stale injected message history: %s", history)
	}
}

func TestLoadConversationFailureAndCancellationPreserveState(t *testing.T) {
	for _, cancelAfterLoad := range []bool{false, true} {
		want := errors.New("gone")
		response := &assistant.ConversationHistoryResponse{}
		response.Data.Type = "conversation-history-response"
		response.Data.Attributes.Messages = []assistant.Message{assistant.AssistantMessage("new", assistant.TextContent("new"))}
		e := engineWithConversation(&historyBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
			if cancelAfterLoad {
				return response, nil
			}
			return nil, want
		}}, "old", assistant.AssistantMessage("old", assistant.TextContent("keep")))
		before := e.Snapshot()
		ctx, cancel := context.WithCancel(context.Background())
		conversation, err := e.LoadConversation(ctx, testConversationID)
		cancel()
		if cancelAfterLoad {
			if err != nil {
				t.Fatal(err)
			}
			want = context.Canceled
			err = e.InstallConversation(ctx, conversation)
		}
		if !errors.Is(err, want) || e.ConversationID() != "old" || !reflect.DeepEqual(e.Snapshot(), before) || e.OperationActive() {
			t.Fatalf("cancelAfterLoad=%v: error=%v id=%q active=%v", cancelAfterLoad, err, e.ConversationID(), e.OperationActive())
		}
	}
}

func TestConversationLoadingRejectsMalformedHistory(t *testing.T) {
	for name, response := range map[string]*assistant.ConversationHistoryResponse{
		"nil response":     nil,
		"missing envelope": {},
		"missing text payload": func() *assistant.ConversationHistoryResponse {
			r := &assistant.ConversationHistoryResponse{}
			r.Data.Type = "conversation-history-response"
			r.Data.Attributes.Messages = []assistant.Message{{
				MessageID: "bad",
				Role:      "assistant",
				Content:   assistant.Content{Type: assistant.ContentMarkdownFragment},
			}}
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			e := engineWithConversation(&historyBackend{resp: response}, "old")
			_, err := e.LoadConversation(context.Background(), testConversationID)
			if !errors.Is(err, ErrMalformedHistory) || e.ConversationID() != "old" {
				t.Fatalf("error/id = %v/%q", err, e.ConversationID())
			}
			e.opts.ConversationID = testConversationID
			events := collect(e.Restore(context.Background()))
			if len(events) != 1 || !errors.Is(events[0].Err, ErrMalformedHistory) {
				t.Fatalf("restore events = %+v", events)
			}
			if e.OperationActive() {
				t.Fatal("malformed result retained engine ownership")
			}
		})
	}
}

// A startup read must not contend with turns: the gate belongs to operations
// that mutate engine state, and listing mutates none.
// hold claims e as an operation does, until the test ends.
func hold(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.idle(func() error { e.busy = true; return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.do(func() { e.busy = false }) })
}

func TestListConversationsIgnoresTheOperationGate(t *testing.T) {
	response := &assistant.UserConversationsResponse{}
	response.Data.Type = "user-conversations-response"
	response.Data.Attributes.Conversations = []assistant.ConversationSummary{
		{ID: testConversationID, ConversationID: testConversationID, Title: "One"},
	}
	e := New(&historyBackend{list: response}, Config{})

	hold(t, e)

	result := e.ListConversations(context.Background())
	if result.Err != nil {
		t.Fatalf("read failed while the gate was held: %v", result.Err)
	}
	if len(result.Conversations) != 1 {
		t.Fatalf("conversations = %+v", result.Conversations)
	}
	if !e.OperationActive() {
		t.Fatal("the ungated read released an operation it never took")
	}
}

func TestConversationLoadIgnoresActiveTurnButInstallRejectsIt(t *testing.T) {
	response := &assistant.ConversationHistoryResponse{}
	response.Data.Type = "conversation-history-response"
	e := New(&historyBackend{resp: response}, Config{ConversationID: "old"})
	hold(t, e)
	conversation, err := e.LoadConversation(context.Background(), testConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.InstallConversation(context.Background(), conversation); !errors.Is(err, ErrOperationActive) {
		t.Fatalf("install overlap = %v", err)
	}
	if events := collect(e.Restore(context.Background())); len(events) != 1 || !errors.Is(events[0].Err, ErrOperationActive) {
		t.Fatalf("restore overlap = %+v", events)
	}
	if !e.OperationActive() || e.ConversationID() != "old" {
		t.Fatal("load or rejected install changed the active operation")
	}
}

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
	e := New(client, Config{})

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
	_ = collect(e.StartTurn(ctx, TurnInput{Message: "Reply with exactly: second conversation"}))
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

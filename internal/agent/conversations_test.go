package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

const testConversationID = "12345678-1234-1234-1234-123456781234"

type conversationBackend struct {
	list    func(context.Context) (*assistant.UserConversationsResponse, error)
	history func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error)
}

func (*conversationBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	return "", nil
}

func (b *conversationBackend) UserConversations(ctx context.Context) (*assistant.UserConversationsResponse, error) {
	return b.list(ctx)
}

func (b *conversationBackend) ConversationHistory(ctx context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	return b.history(ctx, in)
}

func engineWithConversation(backend Backend, id string, messages ...assistant.Message) *Engine {
	e := New(backend, assistant.SendOptions{ConversationID: id, Model: "retained"})
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
	e := New(&conversationBackend{
		list: func(context.Context) (*assistant.UserConversationsResponse, error) { return response, nil },
	}, assistant.SendOptions{})
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

	unsupportedEngine := New(&scriptBackend{}, assistant.SendOptions{})
	unsupported := unsupportedEngine.ListConversations(context.Background())
	if !errors.Is(unsupported.Err, ErrConversationListUnsupported) {
		t.Fatalf("unsupported error = %v", unsupported.Err)
	}
	if unsupportedEngine.OperationActive() {
		t.Fatal("unsupported result retained engine ownership")
	}

	malformed := New(&conversationBackend{
		list: func(context.Context) (*assistant.UserConversationsResponse, error) { return nil, nil },
	}, assistant.SendOptions{})
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
	malformed = New(&conversationBackend{
		list: func(context.Context) (*assistant.UserConversationsResponse, error) { return badIDs, nil },
	}, assistant.SendOptions{})
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
	backend := &conversationBackend{history: func(_ context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
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
		e := engineWithConversation(&conversationBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
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
			e := engineWithConversation(&conversationBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
				return response, nil
			}}, "old")
			_, err := e.LoadConversation(context.Background(), testConversationID)
			if !errors.Is(err, ErrMalformedHistory) || e.ConversationID() != "old" {
				t.Fatalf("error/id = %v/%q", err, e.ConversationID())
			}
			e.opts.ConversationID = testConversationID
			events := drain(e.Restore(context.Background()))
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
func TestListConversationsIgnoresTheOperationGate(t *testing.T) {
	response := &assistant.UserConversationsResponse{}
	response.Data.Type = "user-conversations-response"
	response.Data.Attributes.Conversations = []assistant.ConversationSummary{
		{ID: testConversationID, ConversationID: testConversationID, Title: "One"},
	}
	e := New(&conversationBackend{
		list: func(context.Context) (*assistant.UserConversationsResponse, error) { return response, nil },
	}, assistant.SendOptions{})

	if !e.begin() {
		t.Fatal("could not take the operation gate")
	}
	t.Cleanup(func() { e.active.Store(false) })

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
	e := New(&historyBackend{resp: response}, assistant.SendOptions{ConversationID: "old"})
	if !e.begin() {
		t.Fatal("could not take the operation gate")
	}
	t.Cleanup(func() { e.active.Store(false) })
	conversation, err := e.LoadConversation(context.Background(), testConversationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.InstallConversation(context.Background(), conversation); !errors.Is(err, ErrOperationActive) {
		t.Fatalf("install overlap = %v", err)
	}
	if events := drain(e.Restore(context.Background())); len(events) != 1 || !errors.Is(events[0].Err, ErrOperationActive) {
		t.Fatalf("restore overlap = %+v", events)
	}
	if !e.OperationActive() || e.ConversationID() != "old" {
		t.Fatal("load or rejected install changed the active operation")
	}
}

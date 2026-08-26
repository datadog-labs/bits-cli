package agent

import (
	"context"
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
	result := <-e.ListConversations(context.Background())
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
	unsupported := <-unsupportedEngine.ListConversations(context.Background())
	if !errors.Is(unsupported.Err, ErrConversationListUnsupported) {
		t.Fatalf("unsupported error = %v", unsupported.Err)
	}
	if unsupportedEngine.OperationActive() {
		t.Fatal("unsupported result retained engine ownership")
	}

	malformed := New(&conversationBackend{
		list: func(context.Context) (*assistant.UserConversationsResponse, error) { return nil, nil },
	}, assistant.SendOptions{})
	if got := (<-malformed.ListConversations(context.Background())).Err; !errors.Is(got, ErrMalformedConversationList) {
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
	result = <-malformed.ListConversations(context.Background())
	if result.Err != nil || result.Omitted != 1 || len(result.Conversations) != 1 || result.Conversations[0].ConversationID != testConversationID {
		t.Fatalf("partially malformed result = %+v", result)
	}
	if malformed.OperationActive() {
		t.Fatal("partial list result retained engine ownership")
	}
}

func TestSwitchConversationCommitIsAtomic(t *testing.T) {
	response := &assistant.ConversationHistoryResponse{}
	response.Data.Type = "conversation-history-response"
	response.Data.ID = "response-id-is-not-conversation-id"
	response.Data.Attributes.Messages = []assistant.Message{
		assistant.AssistantMessage("m1", assistant.TextContent("Hello")),
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
	before := e.Snapshot()
	result := <-e.SwitchConversation(context.Background(), " "+testConversationID+" ")
	if result.Err != nil || result.ConversationID != testConversationID || len(result.Blocks) != 2 {
		t.Fatalf("loaded result = %+v", result)
	}
	if e.ConversationID() != "old" || !reflect.DeepEqual(e.Snapshot(), before) {
		t.Fatal("load mutated engine before commit")
	}
	if result.Blocks[0].Markdown.Content != "Hello world" || result.Blocks[1].Kind != assistant.KindUnknown {
		t.Fatalf("loaded blocks = %+v", result.Blocks)
	}
	if !e.OperationActive() {
		t.Fatal("loaded candidate released ownership before commit/discard")
	}
	if err := result.Commit(); err != nil {
		t.Fatal(err)
	}
	if e.OperationActive() {
		t.Fatal("commit returned before releasing engine ownership")
	}
	if e.ConversationID() != testConversationID || !reflect.DeepEqual(e.Snapshot(), result.Blocks) {
		t.Fatal("commit did not atomically install candidate")
	}
}

func TestSwitchConversationDiscardFailureAndCancellationRollback(t *testing.T) {
	t.Run("discard", func(t *testing.T) {
		response := &assistant.ConversationHistoryResponse{}
		response.Data.Type = "conversation-history-response"
		response.Data.Attributes.Messages = []assistant.Message{assistant.AssistantMessage("new", assistant.TextContent("new"))}
		e := engineWithConversation(&conversationBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
			return response, nil
		}}, "old", assistant.AssistantMessage("old", assistant.TextContent("keep")))
		before := e.Snapshot()
		result := <-e.SwitchConversation(context.Background(), testConversationID)
		if err := result.Discard(); err != nil {
			t.Fatal(err)
		}
		if e.OperationActive() {
			t.Fatal("discard returned before releasing engine ownership")
		}
		if e.ConversationID() != "old" || !reflect.DeepEqual(e.Snapshot(), before) {
			t.Fatal("discard mutated engine")
		}
	})

	t.Run("backend failure", func(t *testing.T) {
		want := errors.New("gone")
		e := engineWithConversation(&conversationBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
			return nil, want
		}}, "old", assistant.AssistantMessage("old", assistant.TextContent("keep")))
		before := e.Snapshot()
		result := <-e.SwitchConversation(context.Background(), testConversationID)
		if !errors.Is(result.Err, want) || e.ConversationID() != "old" || !reflect.DeepEqual(e.Snapshot(), before) {
			t.Fatalf("failure result/state = %+v/%q", result, e.ConversationID())
		}
		if e.OperationActive() {
			t.Fatal("failure result retained engine ownership")
		}
	})

	t.Run("cancel after load before commit", func(t *testing.T) {
		response := &assistant.ConversationHistoryResponse{}
		response.Data.Type = "conversation-history-response"
		response.Data.Attributes.Messages = []assistant.Message{assistant.AssistantMessage("new", assistant.TextContent("new"))}
		e := engineWithConversation(&conversationBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
			return response, nil
		}}, "old", assistant.AssistantMessage("old", assistant.TextContent("keep")))
		before := e.Snapshot()
		ctx, cancel := context.WithCancel(context.Background())
		result := <-e.SwitchConversation(ctx, testConversationID)
		cancel()
		if err := result.Commit(); !errors.Is(err, context.Canceled) {
			t.Fatalf("commit error = %v", err)
		}
		if e.OperationActive() {
			t.Fatal("cancelled commit returned before releasing ownership")
		}
		if e.ConversationID() != "old" || !reflect.DeepEqual(e.Snapshot(), before) {
			t.Fatal("cancelled candidate mutated engine")
		}
	})
}

func TestSwitchConversationRejectsMalformedHistory(t *testing.T) {
	for name, response := range map[string]*assistant.ConversationHistoryResponse{
		"nil response":     nil,
		"missing envelope": &assistant.ConversationHistoryResponse{},
		"missing content type": func() *assistant.ConversationHistoryResponse {
			r := &assistant.ConversationHistoryResponse{}
			r.Data.Type = "conversation-history-response"
			r.Data.Attributes.Messages = []assistant.Message{{MessageID: "bad"}}
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			e := engineWithConversation(&conversationBackend{history: func(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
				return response, nil
			}}, "old")
			result := <-e.SwitchConversation(context.Background(), testConversationID)
			if !errors.Is(result.Err, ErrMalformedHistory) || e.ConversationID() != "old" {
				t.Fatalf("result/id = %+v/%q", result, e.ConversationID())
			}
			if e.OperationActive() {
				t.Fatal("malformed result retained engine ownership")
			}
		})
	}
}

func TestSwitchSameConversationReleasesBeforeResult(t *testing.T) {
	e := engineWithConversation(&scriptBackend{}, testConversationID,
		assistant.AssistantMessage("old", assistant.TextContent("keep")),
	)
	result := <-e.SwitchConversation(context.Background(), testConversationID)
	if result.Err != nil || result.ConversationID != testConversationID {
		t.Fatalf("same-conversation result = %+v", result)
	}
	if e.OperationActive() {
		t.Fatal("same-conversation result published before releasing ownership")
	}
}

func TestLifecycleOperationsRejectOverlap(t *testing.T) {
	gate := make(chan struct{})
	e := New(&blockingBackend{gate: gate}, assistant.SendOptions{ConversationID: "old"})
	turn := e.StartTurn(context.Background(), TurnInput{Message: "busy"})
	if got := (<-e.ListConversations(context.Background())).Err; !errors.Is(got, ErrOperationActive) {
		t.Fatalf("list overlap = %v", got)
	}
	if got := (<-e.SwitchConversation(context.Background(), testConversationID)).Err; !errors.Is(got, ErrOperationActive) {
		t.Fatalf("switch overlap = %v", got)
	}
	if events := drain(e.Restore(context.Background())); len(events) != 1 || !errors.Is(events[0].Err, ErrOperationActive) {
		t.Fatalf("restore overlap = %+v", events)
	}
	close(gate)
	_ = drain(turn)
}

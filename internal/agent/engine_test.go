package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// scriptBackend replays a fixed set of messages, then returns convID/err.
type scriptBackend struct {
	msgs   []assistant.Message
	convID string
	err    error
}

func (b *scriptBackend) Send(_ context.Context, _ any, _ assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	for _, m := range b.msgs {
		var ar assistant.AssistantResponse
		ar.Data.Attributes.StructuredMessage = m
		if err := fn(ar); err != nil {
			return "", err
		}
	}
	return b.convID, b.err
}

// blockingBackend holds a turn open until gate is closed.
type blockingBackend struct{ gate chan struct{} }

func (b *blockingBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	select {
	case <-b.gate:
	case <-ctx.Done():
	}
	return "conv", nil
}

type conversationRecordingBackend struct {
	opts     []assistant.SendOptions
	messages map[string][]string
}

type cancellationIDBackend struct{ started chan struct{} }

func (b *cancellationIDBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	close(b.started)
	<-ctx.Done()
	return "created-before-cancel", ctx.Err()
}

func (b *conversationRecordingBackend) Send(_ context.Context, message any, opts assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.opts = append(b.opts, opts)
	id := opts.ConversationID
	if id == "" {
		id = "new-conversation-1"
	}
	b.messages[id] = append(b.messages[id], message.(string))
	return id, nil
}

// historyBackend also serves conversation history.
type historyBackend struct {
	scriptBackend
	resp *assistant.ConversationHistoryResponse
	err  error
}

func (b *historyBackend) ConversationHistory(_ context.Context, _ assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	return b.resp, b.err
}

func drain(ch <-chan Event) []Event {
	var evs []Event
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func kinds(evs []Event) []EventKind {
	ks := make([]EventKind, len(evs))
	for i, e := range evs {
		ks[i] = e.Kind
	}
	return ks
}

func TestTurnCompletionSynchronizesOperationRelease(t *testing.T) {
	e := New(&scriptBackend{convID: "conversation-1"}, assistant.SendOptions{})
	operation := e.beginTurn(context.Background(), TurnInput{Message: "question"})
	completion := <-operation.completion
	if !completion.Completed {
		t.Fatalf("completion = %+v", completion)
	}
	if e.OperationActive() {
		t.Fatal("completion was published before operation release")
	}
	if err := e.NewConversation(); err != nil {
		t.Fatalf("operation after completion: %v", err)
	}
	_ = drain(operation.events)
}

func TestConcurrentTurnReturnsOperationError(t *testing.T) {
	gate := make(chan struct{})
	e := New(&blockingBackend{gate: gate}, assistant.SendOptions{})
	ch := e.StartTurn(context.Background(), TurnInput{Message: "one"})
	events := drain(e.StartTurn(context.Background(), TurnInput{Message: "two"}))
	if len(events) != 1 || events[0].Kind != EventError || !errors.Is(events[0].Err, ErrOperationActive) {
		t.Fatalf("overlap events = %+v, want ErrOperationActive", events)
	}
	close(gate)
	_ = drain(ch)
}

func TestTurnEmitsEventSequence(t *testing.T) {
	usage := assistant.AssistantMessage("m1", assistant.TextContent(""))
	usage.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 5}}
	b := &scriptBackend{
		convID: "conv-1",
		msgs: []assistant.Message{
			assistant.AssistantMessage("m1", assistant.TextContent("Hello")),
			assistant.AssistantMessage("m1", assistant.TextContent(" world")),
			usage,
		},
	}
	e := New(b, assistant.SendOptions{})

	evs := drain(e.StartTurn(context.Background(), TurnInput{Message: "hi"}))
	want := []EventKind{EventBlock, EventBlock, EventBlock, EventUsage, EventConversation, EventTurnDone}
	if got := kinds(evs); !reflect.DeepEqual(got, want) {
		t.Fatalf("event kinds = %v, want %v", got, want)
	}
	if changed := evs[2].Update.Changed; changed.Markdown == nil || changed.Markdown.Content != "Hello world" {
		t.Fatalf("final text block = %+v", changed)
	}
	if evs[4].ConvID != "conv-1" {
		t.Fatalf("conv id = %q, want conv-1", evs[4].ConvID)
	}
}

func TestTurnRetainsConversationIDDiscoveredBeforeBackendError(t *testing.T) {
	backendErr := errors.New("stream failed")
	e := New(&scriptBackend{
		convID: "created-before-error",
		err:    backendErr,
		msgs:   []assistant.Message{assistant.AssistantMessage("m1", assistant.TextContent("partial"))},
	}, assistant.SendOptions{})

	events := drain(e.StartTurn(context.Background(), TurnInput{Message: "question"}))
	wantKinds := []EventKind{EventBlock, EventBlock, EventConversation, EventError}
	if got := kinds(events); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("event kinds = %v, want %v", got, wantKinds)
	}
	if events[2].ConvID != "created-before-error" || !errors.Is(events[3].Err, backendErr) {
		t.Fatalf("terminal events = %+v", events[2:])
	}
	if got := e.ConversationID(); got != "created-before-error" {
		t.Fatalf("conversation ID = %q", got)
	}
	blocks := e.Snapshot()
	if len(blocks) != 2 || !blocks[1].Complete {
		t.Fatalf("final blocks = %+v", blocks)
	}
}

func TestNewConversationIsLazyAndRetainsClientConfiguration(t *testing.T) {
	backend := &conversationRecordingBackend{messages: map[string][]string{}}
	opts := assistant.SendOptions{
		ConversationID:    "old-conversation",
		Model:             "retained-model",
		DebugTag:          "retained-debug-tag",
		CustomUserContext: "retained-context",
		MessageHistory:    []json.RawMessage{json.RawMessage(`{"role":"user","content":"old history"}`)},
	}
	e := New(backend, opts)

	_ = drain(e.StartTurn(context.Background(), TurnInput{Message: "old prompt"}))
	callsBeforeReset := len(backend.opts)
	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if len(backend.opts) != callsBeforeReset {
		t.Fatal("NewConversation made a backend request")
	}
	if got := e.ConversationID(); got != "" {
		t.Fatalf("conversation id after reset = %q, want empty", got)
	}
	if got := e.PreviousConversationID(); got != "old-conversation" {
		t.Fatalf("previous conversation id = %q, want old-conversation", got)
	}
	if len(e.transcript.Blocks()) != 0 {
		t.Fatalf("transcript after reset has %d blocks", len(e.transcript.Blocks()))
	}

	_ = drain(e.StartTurn(context.Background(), TurnInput{Message: "new prompt"}))
	if got := e.ConversationID(); got != "new-conversation-1" {
		t.Fatalf("new conversation id = %q", got)
	}
	if got := backend.opts[len(backend.opts)-1]; got.ConversationID != "" ||
		len(got.MessageHistory) != 0 || got.Model != opts.Model ||
		got.DebugTag != opts.DebugTag || got.CustomUserContext != opts.CustomUserContext {
		t.Fatalf("first new request options = %+v; process-wide configuration was not retained", got)
	}
	if got := backend.messages["old-conversation"]; !reflect.DeepEqual(got, []string{"old prompt"}) {
		t.Fatalf("prior conversation was mutated: %v", got)
	}
	if got := backend.messages["new-conversation-1"]; !reflect.DeepEqual(got, []string{"new prompt"}) {
		t.Fatalf("new conversation messages = %v", got)
	}
}

func TestNewConversationRetainsIDDiscoveredDuringCancellation(t *testing.T) {
	backend := &cancellationIDBackend{started: make(chan struct{})}
	e := New(backend, assistant.SendOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	turn := e.StartTurn(ctx, TurnInput{Message: "cancel me"})
	<-backend.started
	cancel()
	_ = drain(turn)

	if got := e.ConversationID(); got != "created-before-cancel" {
		t.Fatalf("conversation id after cancellation = %q", got)
	}
	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if got := e.PreviousConversationID(); got != "created-before-cancel" {
		t.Fatalf("previous conversation id after reset = %q", got)
	}
	if e.ConversationID() != "" {
		t.Fatalf("current conversation id after reset = %q", e.ConversationID())
	}
}

func TestNewConversationRejectsActiveTurnWithoutMutation(t *testing.T) {
	gate := make(chan struct{})
	e := New(&blockingBackend{gate: gate}, assistant.SendOptions{ConversationID: "old"})
	turn := e.StartTurn(context.Background(), TurnInput{Message: "busy"})
	if err := e.NewConversation(); !errors.Is(err, ErrOperationActive) {
		t.Fatalf("NewConversation error = %v, want ErrOperationActive", err)
	}
	if got := e.ConversationID(); got != "old" {
		t.Fatalf("active reset changed conversation id to %q", got)
	}
	close(gate)
	_ = drain(turn)
}

func TestRestoreEmitsSingleSnapshot(t *testing.T) {
	resp := &assistant.ConversationHistoryResponse{}
	resp.Data.Attributes.Messages = []assistant.Message{
		assistant.AssistantMessage("m1", assistant.TextContent("Hello")),
		assistant.AssistantMessage("turn", assistant.Content{Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: "ended"}}),
		assistant.AssistantMessage("stop", assistant.Content{Type: assistant.ContentUserStop, Stop: &assistant.StopPayload{Content: "stopped"}}),
		assistant.AssistantMessage("internal", assistant.Content{Type: assistant.ContentProviderCompaction, Compaction: &assistant.CompactionPayload{Summary: "private"}}),
		assistant.AssistantMessage("m2", assistant.TextContent("World")),
		assistant.AssistantMessage("future", assistant.Content{Type: "future_content"}),
	}
	e := New(&historyBackend{resp: resp}, assistant.SendOptions{ConversationID: "conv-1"})

	evs := drain(e.Restore(context.Background()))
	if len(evs) != 1 || evs[0].Kind != EventBlock {
		t.Fatalf("events = %v, want one EventBlock", kinds(evs))
	}
	blocks := evs[0].Update.Blocks
	if len(blocks) != 3 || blocks[0].Kind != assistant.KindText || blocks[1].Kind != assistant.KindText || blocks[2].Kind != assistant.KindUnknown {
		t.Fatalf("restored blocks = %+v, want text/text/unknown without technical markers", blocks)
	}
}

func TestRestoreUnsupportedBackendErrors(t *testing.T) {
	e := New(&scriptBackend{}, assistant.SendOptions{ConversationID: "conv-1"})

	evs := drain(e.Restore(context.Background()))
	if len(evs) != 1 || evs[0].Kind != EventError || !errors.Is(evs[0].Err, ErrHistoryUnsupported) {
		t.Fatalf("events = %+v, want ErrHistoryUnsupported", evs)
	}
}

// An empty history folds to no blocks, so the len>0 guard emits nothing.
func TestRestoreEmptyEmitsNothing(t *testing.T) {
	e := New(&historyBackend{resp: &assistant.ConversationHistoryResponse{}}, assistant.SendOptions{ConversationID: "conv-1"})

	if evs := drain(e.Restore(context.Background())); len(evs) != 0 {
		t.Fatalf("events = %v, want none", kinds(evs))
	}
}

package agent

import (
	"context"
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

func TestConcurrentTurnPanics(t *testing.T) {
	gate := make(chan struct{})
	e := New(&blockingBackend{gate: gate}, assistant.SendOptions{})
	ch := e.StartTurn(context.Background(), "one")
	defer func() {
		close(gate)
		for range ch {
		}
	}()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on overlapping turn")
		}
	}()
	_ = e.StartTurn(context.Background(), "two")
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

	evs := drain(e.StartTurn(context.Background(), "hi"))
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

func TestRestoreEmitsSingleSnapshot(t *testing.T) {
	resp := &assistant.ConversationHistoryResponse{}
	resp.Data.Attributes.Messages = []assistant.Message{
		assistant.AssistantMessage("m1", assistant.TextContent("Hello")),
		assistant.AssistantMessage("m2", assistant.TextContent("World")),
	}
	e := New(&historyBackend{resp: resp}, assistant.SendOptions{ConversationID: "conv-1"})

	evs := drain(e.Restore(context.Background()))
	if len(evs) != 1 || evs[0].Kind != EventBlock {
		t.Fatalf("events = %v, want one EventBlock", kinds(evs))
	}
	if n := len(evs[0].Update.Blocks); n != 2 {
		t.Fatalf("restored blocks = %d, want 2", n)
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

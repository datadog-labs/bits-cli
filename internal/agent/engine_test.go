package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

// stub is an agent.Backend that replays a fixed list of messages.
type stub struct {
	msgs   []assistant.Message
	convID string
	err    error
}

func (s *stub) Send(_ context.Context, _ any, _ assistant.SendOptions,
	fn func(assistant.AssistantResponse) error,
) (string, error) {
	for _, m := range s.msgs {
		var ar assistant.AssistantResponse
		ar.Data.Attributes.ConversationID = s.convID
		ar.Data.Attributes.StructuredMessage = m
		if err := fn(ar); err != nil {
			return s.convID, err
		}
	}
	return s.convID, s.err
}

// Every message reaches the consumer untouched, including content kinds the
// engine has no opinion about, followed by the synthesized lifecycle events.
func TestEngine_PassesMessagesThroughVerbatim(t *testing.T) {
	redacted := assistant.ThinkingContent("hmm")
	redacted.Thinking.Redacted = true

	in := []assistant.Message{
		assistant.AssistantMessage("m1", redacted),
		assistant.AssistantMessage("m2", assistant.TextContent("hi")),
		assistant.AssistantMessage("m3", assistant.Content{
			Type:      assistant.ContentDashboard,
			Dashboard: &assistant.DashboardPayload{Title: "gen"},
		}),
	}
	e := agent.New(&stub{msgs: in, convID: "conv-1"}, assistant.SendOptions{})

	var got []assistant.Message
	var kinds []agent.EventKind
	for ev := range e.Start(context.Background(), "hello") {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == agent.EventMessage {
			got = append(got, ev.Msg)
		}
	}

	if len(got) != len(in) {
		t.Fatalf("got %d messages, want %d", len(got), len(in))
	}
	if !got[0].Content.Thinking.Redacted {
		t.Error("thinking redacted flag lost in transit")
	}
	if got[2].Content.Dashboard == nil || got[2].Content.Dashboard.Title != "gen" {
		t.Error("dashboard payload lost in transit")
	}
	for i := range in {
		if got[i].MessageID != in[i].MessageID || got[i].Content.Type != in[i].Content.Type {
			t.Errorf("message %d = (%s, %s), want (%s, %s)",
				i, got[i].MessageID, got[i].Content.Type, in[i].MessageID, in[i].Content.Type)
		}
	}

	want := []agent.EventKind{
		agent.EventMessage, agent.EventMessage, agent.EventMessage,
		agent.EventConversation, agent.EventTurnDone,
	}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event kinds = %v, want %v", kinds, want)
		}
	}
}

// historyStub is a Backend that also loads a fixed conversation history.
type historyStub struct {
	stub
	history []assistant.Message
	histErr error
	gotID   string
}

func (h *historyStub) ConversationHistory(_ context.Context, id string) (*assistant.ConversationHistoryResponse, error) {
	h.gotID = id
	if h.histErr != nil {
		return nil, h.histErr
	}
	var resp assistant.ConversationHistoryResponse
	resp.Data.Attributes.Messages = h.history
	return &resp, nil
}

func TestEngine_LoadHistoryReturnsPersistedMessages(t *testing.T) {
	want := []assistant.Message{
		{Role: "user", MessageID: "u1", Content: assistant.TextContent("hello")},
		assistant.AssistantMessage("a1", assistant.TextContent("hi there")),
	}
	b := &historyStub{history: want}
	e := agent.New(b, assistant.SendOptions{ConversationID: "conv-42"})

	got, err := e.LoadHistory(context.Background())
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if b.gotID != "conv-42" {
		t.Errorf("loaded id = %q, want conv-42", b.gotID)
	}
	if len(got) != len(want) || got[0].MessageID != "u1" || got[1].MessageID != "a1" {
		t.Fatalf("history = %+v, want %+v", got, want)
	}
}

// With no conversation to restore, LoadHistory is a no-op and never touches the
// backend (so a backend without history support is fine).
func TestEngine_LoadHistoryNoConversationIsNoop(t *testing.T) {
	e := agent.New(&stub{}, assistant.SendOptions{})
	got, err := e.LoadHistory(context.Background())
	if err != nil || got != nil {
		t.Fatalf("LoadHistory = (%v, %v), want (nil, nil)", got, err)
	}
}

// A backend that cannot load history (the fake) errors only when a conversation
// is actually requested.
func TestEngine_LoadHistoryUnsupportedBackendErrors(t *testing.T) {
	e := agent.New(&stub{}, assistant.SendOptions{ConversationID: "conv-1"})
	if _, err := e.LoadHistory(context.Background()); err == nil {
		t.Fatal("want error for backend without history support")
	}
}

func TestEngine_BackendErrorBecomesErrorEvent(t *testing.T) {
	boom := errors.New("boom")
	e := agent.New(&stub{err: boom}, assistant.SendOptions{})

	var last agent.Event
	for ev := range e.Start(context.Background(), "hello") {
		last = ev
	}
	if last.Kind != agent.EventError || !errors.Is(last.Err, boom) {
		t.Fatalf("last event = %v (%v), want EventError wrapping boom", last.Kind, last.Err)
	}
}

// Cancelling ends the turn quietly: the channel closes with no error event.
func TestEngine_CancelEndsTurnQuietly(t *testing.T) {
	e := agent.New(&stub{convID: "conv-1"}, assistant.SendOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for ev := range e.Start(ctx, "hello") {
		if ev.Kind == agent.EventError {
			t.Fatalf("cancel produced an error event: %v", ev.Err)
		}
	}
}

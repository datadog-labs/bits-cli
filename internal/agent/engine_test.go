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

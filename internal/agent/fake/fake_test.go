package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// collect runs one turn and returns the streamed (type, text) pairs.
func collect(t *testing.T, message string) [][2]string {
	t.Helper()
	f := &Fake{} // no delay
	var got [][2]string
	_, err := f.Send(context.Background(), message, assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
		c := ar.Data.Attributes.StructuredMessage.Content
		got = append(got, [2]string{c.Type, c.TextBody()})
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return got
}

func TestDeterministicPerSeed(t *testing.T) {
	a := collect(t, "why is latency high?")
	b := collect(t, "why is latency high?")
	if len(a) != len(b) {
		t.Fatalf("nondeterministic length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic fragment %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	if len(collect(t, "one")) == 0 {
		t.Fatal("expected output")
	}
	// Extremely unlikely to be identical streams for different inputs.
	a, b := collect(t, "alpha"), collect(t, "bravo charlie delta")
	same := len(a) == len(b)
	for i := 0; same && i < len(a); i++ {
		if a[i] != b[i] {
			same = false
		}
	}
	if same {
		t.Fatal("different inputs produced identical streams")
	}
}

func TestStreamsTextAndUsage(t *testing.T) {
	f := &Fake{}
	var text, usage int
	_, err := f.Send(context.Background(), "hello", assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
		msg := ar.Data.Attributes.StructuredMessage
		if msg.Content.Type == assistant.ContentMarkdownFragment && msg.Content.TextBody() != "" {
			text++
		}
		if msg.Results != nil && msg.Results.Usage != nil {
			usage++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if text == 0 {
		t.Error("expected at least one text fragment")
	}
	if usage != 1 {
		t.Errorf("expected exactly one usage line, got %d", usage)
	}
}

func TestReturnsConversationID(t *testing.T) {
	f := &Fake{}
	id, err := f.Send(context.Background(), "hi", assistant.SendOptions{ConversationID: "abc"}, func(assistant.AssistantResponse) error {
		return nil
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "abc" {
		t.Errorf("conversation id = %q, want abc (echoed)", id)
	}
}

func TestMessageIDsUniquePerTurn(t *testing.T) {
	// Regression: the message id must not derive from the prompt, or identical
	// prompts across turns collide and clobber earlier transcript items.
	f := &Fake{}
	id := func() string {
		var got string
		_, err := f.Send(context.Background(), "same prompt", assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
			if got == "" {
				got = ar.Data.Attributes.StructuredMessage.MessageID
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if a, b := id(), id(); a == b {
		t.Fatalf("message id must differ across turns, got %q twice", a)
	}
}

func TestMultipleUniqueToolCalls(t *testing.T) {
	f := &Fake{}
	maxCalls := 0
	for _, msg := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		seen := map[string]bool{}
		calls := 0
		_, err := f.Send(context.Background(), msg, assistant.SendOptions{}, func(ar assistant.AssistantResponse) error {
			c := ar.Data.Attributes.StructuredMessage.Content
			if c.Type == assistant.ContentToolCall && c.Tool != nil {
				calls++
				if seen[c.Tool.ToolCallID] {
					t.Fatalf("duplicate tool call id %q in one turn (%q)", c.Tool.ToolCallID, msg)
				}
				seen[c.Tool.ToolCallID] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		maxCalls = max(maxCalls, calls)
	}
	if maxCalls < 2 {
		t.Fatalf("expected some turn with multiple tool calls, max was %d", maxCalls)
	}
}

func TestContextCancellation(t *testing.T) {
	f := &Fake{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before streaming

	var n int
	_, err := f.Send(ctx, "hello", assistant.SendOptions{}, func(assistant.AssistantResponse) error {
		n++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if n != 0 {
		t.Errorf("streamed %d fragments after cancel, want 0", n)
	}
}

func TestPropagatesCallbackError(t *testing.T) {
	f := &Fake{}
	boom := errors.New("boom")
	_, err := f.Send(context.Background(), "hello", assistant.SendOptions{}, func(assistant.AssistantResponse) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected callback error to propagate, got %v", err)
	}
}

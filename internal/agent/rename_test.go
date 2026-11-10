package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type renameBackend struct {
	scriptBackend
	inputs []assistant.RenameConversationInput
	err    error
}

func (b *renameBackend) RenameConversation(_ context.Context, in assistant.RenameConversationInput) error {
	b.inputs = append(b.inputs, in)
	return b.err
}

func TestRenameConversation(t *testing.T) {
	b := &renameBackend{}
	e := New(b, Config{ConversationID: testConversationID})
	defer e.Close()
	if err := e.RenameConversation(context.Background(), testConversationID, "Prod outage"); err != nil {
		t.Fatal(err)
	}
	want := assistant.RenameConversationInput{ConversationID: testConversationID, Title: "Prod outage"}
	if len(b.inputs) != 1 || b.inputs[0] != want {
		t.Fatalf("inputs = %+v, want [%+v]", b.inputs, want)
	}

	b.err = errors.New("offline")
	if err := e.RenameConversation(context.Background(), testConversationID, "x"); !errors.Is(err, b.err) {
		t.Fatalf("err = %v, want the backend error", err)
	}
}

func TestRenameConversationValidation(t *testing.T) {
	b := &renameBackend{}
	e := New(b, Config{})
	defer e.Close()
	if err := e.RenameConversation(context.Background(), "bad", "x"); !errors.Is(err, ErrInvalidConversationID) || len(b.inputs) != 0 {
		t.Fatalf("err = %v, calls = %d", err, len(b.inputs))
	}

	plain := New(&scriptBackend{}, Config{})
	defer plain.Close()
	if err := plain.RenameConversation(context.Background(), testConversationID, "x"); err == nil {
		t.Fatal("a backend without rename support accepted the call")
	}
}

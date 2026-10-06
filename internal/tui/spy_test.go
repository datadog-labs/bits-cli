package tui

import (
	"context"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// spyBackend is an agent.Backend whose Send fails the test if called. It lets
// submit-path tests assert the "no backend request" acceptance criterion
// explicitly: if a slash command (or busy-guarded ordinary input) ever falls
// through to StartTurn, Send runs and the test fails with a clear cause instead
// of nil-panicking on an unset engine.
type spyBackend struct {
	t    *testing.T
	site string
}

func (s *spyBackend) BackendStatus() assistant.BackendStatus {
	return assistant.BackendStatus{Site: s.site}
}

func (s *spyBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	s.t.Fatal("backend Send was called; control-plane input leaked to the model")
	return "", nil
}

// newModelWithSpy builds a fully-constructed Model whose engine is backed by
// a failing spy, so submit-path tests assert the "no backend request"
// acceptance criterion explicitly. Using New (not a bare &Model{}) keeps the
// list and styles non-nil, so a regression that falls through to StartTurn
// reaches spy.Send and fails with a clear cause instead of nil-panicking first.
func newModelWithSpy(t *testing.T) *Model {
	t.Helper()
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}))
	m.editor.Focus()
	return m
}

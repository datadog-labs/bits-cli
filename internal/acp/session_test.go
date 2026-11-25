package acp

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
)

// quietClient accepts every update; a test that asks it anything panics.
type quietClient struct{ client }

func (quietClient) SessionUpdate(context.Context, acp.SessionNotification) error { return nil }

func testSession(t *testing.T) *session {
	t.Helper()
	engine := agent.New(&fake.Fake{}, agent.Config{})
	log := slog.New(slog.DiscardHandler)
	closed := 0
	env := Env{Engine: engine, Close: func() {
		if closed++; closed > 1 {
			t.Error("the session's environment closed twice")
		}
	}}
	return newSession("s", quietClient{}, env, t.TempDir(), log)
}

// run starts a turn and returns once the engine has ended it, before anyone
// waits on it through the session.
func run(t *testing.T, s *session, script string) agent.RunID {
	t.Helper()
	events, stop := s.env.Engine.Subscribe()
	defer stop()
	id, err := s.env.Engine.StartTurn(context.Background(), agent.TurnInput{Message: script})
	if err != nil {
		t.Fatal(err)
	}
	for range agent.Follow(events, id) {
	}
	return id
}

// within fails the test if wait does not return in time: a lost end hangs
// its waiter forever.
func within(t *testing.T, s *session, id agent.RunID) agent.Event {
	t.Helper()
	got := make(chan agent.Event, 1)
	go func() { got <- s.wait(id) }()
	select {
	case end := <-got:
		return end
	case <-time.After(5 * time.Second):
		t.Fatalf("run %d: its end was lost", id)
		return agent.Event{}
	}
}

func TestWaitGetsEachRunsEnd(t *testing.T) {
	s := testSession(t)
	t.Cleanup(s.close)
	// Both runs end before their waiters ask, the later one asks first.
	first := run(t, s, `say("one")`)
	second := run(t, s, `say("two")`)
	for _, id := range []agent.RunID{second, first} {
		if end := within(t, s, id); end.Run != id || end.Kind != agent.EventDone {
			t.Fatalf("run %d ended with %+v", id, end)
		}
	}
}

func TestWaitEndsWithTheSession(t *testing.T) {
	s := testSession(t)
	s.close()
	if end := within(t, s, 1); end.Kind != agent.EventError {
		t.Fatalf("wait on a closed session: %+v", end)
	}
}

func TestDropClosesOnce(t *testing.T) {
	a := NewAgent(Config{})
	s := testSession(t)
	a.sessions[s.id] = s
	// A close request and a failed load can both drop the session.
	a.drop(s)
	a.drop(s)
	a.Close()
}

func TestPromptReplacesTheRunningOne(t *testing.T) {
	h := connect(t, &fake.Fake{}, t.TempDir(), acp.ClientCapabilities{})
	id := h.newSession().SessionId
	first := make(chan acp.StopReason, 1)
	go func() { first <- h.prompt(id, `say("a"); sleep("10s")`) }()
	h.client.awaitText(t, "a")
	// A client may send the next prompt without cancelling: the SDK cancels
	// the running one, which still winds down when the next one arrives.
	if reason := h.prompt(id, `say("b")`); reason != acp.StopReasonEndTurn {
		t.Fatalf("second prompt: stop reason %q", reason)
	}
	if reason := <-first; reason != acp.StopReasonCancelled {
		t.Fatalf("first prompt: stop reason %q", reason)
	}
}

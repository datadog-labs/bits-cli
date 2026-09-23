package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

func TestSubmitLogoutIsLocalAndInvalidatesEngine(t *testing.T) {
	calls := 0
	tools := &agent.ToolSet{}
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{ConversationID: "conversation-1"}), Config{
		Tools: tools,
		Logout: func(context.Context) (bool, error, error) {
			calls++
			return true, nil, nil
		},
	})
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/logout"})

	_, cmd := m.submit()
	if cmd == nil {
		t.Fatal("logout did not start")
	}
	msg := cmd()
	_, quit := m.Update(msg)
	if calls != 1 {
		t.Fatalf("logout calls = %d, want 1", calls)
	}
	if quit == nil {
		t.Fatal("successful logout did not quit")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("logout command = %T, want tea.QuitMsg", quit())
	}
	if m.engine != nil || m.ConversationID() != "" {
		t.Fatal("authenticated engine or conversation survived logout")
	}
	if m.tools != tools || m.logout == nil {
		t.Fatal("logout discarded safe process configuration")
	}
	if !m.LoggedOut() {
		t.Fatal("successful logout was not recorded")
	}
}

func TestLogoutDuringActiveTurnCancelsAndDrainsBeforeDeleting(t *testing.T) {
	logoutCalled := false
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}), Config{
		Logout: func(context.Context) (bool, error, error) {
			logoutCalled = true
			return true, nil, nil
		},
	})
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	m.cancelTurn = cancelTurn
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = chat.PhaseStreaming

	_, cmd := m.dispatchCommand("logout", "")
	if cmd == nil || !m.pendingLogout {
		t.Fatal("active-turn logout was not queued")
	}
	if turnCtx.Err() == nil {
		t.Fatal("active turn was not cancelled")
	}
	if logoutCalled {
		t.Fatal("credentials were deleted before the turn drained")
	}

	_, cmd = m.handleTurnClosed(turnClosedMsg{generation: m.turnGen})
	if cmd == nil || !m.logoutRunning {
		t.Fatal("logout did not start after the turn closed")
	}
	msg := cmd()
	_, quit := m.Update(msg)
	if !logoutCalled {
		t.Fatal("logout action was not called after drain")
	}
	if quit == nil {
		t.Fatal("successful drained logout did not quit")
	}
}

func TestLogoutLocalFailureKeepsAuthenticatedEngine(t *testing.T) {
	want := errors.New("credential store unavailable")
	engine := agent.New(&spyBackend{t: t}, assistant.SendOptions{})
	m := New(engine, Config{
		Logout: func(context.Context) (bool, error, error) {
			return true, nil, want
		},
	})

	_, cmd := m.dispatchCommand("logout", "")
	msg := cmd()
	_, next := m.Update(msg)
	if m.engine != engine {
		t.Fatal("local deletion failure invalidated the usable engine")
	}
	if m.logoutRunning {
		t.Fatal("logout remained active after failure")
	}
	if !errors.Is(m.notice.Err, want) || m.notice.Level != chat.NoticeError {
		t.Fatalf("failure notice = %+v", m.notice)
	}
	if next == nil {
		t.Fatal("failure notice did not schedule expiry")
	}
	if m.LoggedOut() {
		t.Fatal("failed local logout recorded success")
	}
}

func TestAlreadyLoggedOutIsSafeAndInvalidatesEngine(t *testing.T) {
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}), Config{
		Logout: func(context.Context) (bool, error, error) {
			return false, nil, nil
		},
	})

	_, cmd := m.dispatchCommand("logout", "")
	_, quit := m.Update(cmd())
	if !m.LoggedOut() {
		t.Fatal("already-logged-out result was not recorded")
	}
	if m.engine != nil || quit == nil {
		t.Fatal("already-logged-out command did not invalidate and quit")
	}
}

func TestRemoteRevocationFailureStillInvalidatesLocalSession(t *testing.T) {
	want := errors.New("remote revocation unavailable")
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}), Config{
		Logout: func(context.Context) (bool, error, error) {
			return true, want, nil
		},
	})

	_, cmd := m.dispatchCommand("logout", "")
	_, quit := m.Update(cmd())
	if !m.LoggedOut() {
		t.Fatal("revocation failure did not record local logout")
	}
	if m.engine != nil || quit == nil {
		t.Fatal("revocation failure kept the authenticated client alive")
	}
}

func TestQuitCancelsLogout(t *testing.T) {
	started := make(chan struct{})
	done := make(chan struct{})
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}), Config{
		Logout: func(ctx context.Context) (bool, error, error) {
			close(started)
			<-ctx.Done()
			close(done)
			return false, nil, ctx.Err()
		},
	})

	_, cmd := m.dispatchCommand("logout", "")
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	<-started
	_, quit := m.quit()
	if quit == nil {
		t.Fatal("quit did not return tea.Quit")
	}
	<-done
	<-result
}

func TestRepeatedLogoutWhileRunningDoesNotStartAnotherDelete(t *testing.T) {
	calls := 0
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}), Config{
		Logout: func(context.Context) (bool, error, error) {
			calls++
			return true, nil, nil
		},
	})

	_, first := m.dispatchCommand("logout", "")
	_, second := m.dispatchCommand("logout", "")
	if second == nil || m.notice.Empty() {
		t.Fatal("repeated logout did not report in-progress state")
	}
	_ = first()
	if calls != 1 {
		t.Fatalf("logout calls = %d, want 1", calls)
	}
}

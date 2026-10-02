package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
	m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancelTurn}
	m.chatPhase = chat.PhaseStreaming

	// A /new queued first must not swallow the logout.
	_, _ = m.dispatchCommand("new", "")
	_, _ = m.dispatchCommand("logout", "")
	if m.op.then != thenLogout {
		t.Fatal("active-turn logout was not queued")
	}
	if turnCtx.Err() == nil {
		t.Fatal("active turn was not cancelled")
	}
	if logoutCalled {
		t.Fatal("credentials were deleted before the turn drained")
	}

	_, cmd := m.handleTurnClosed(turnClosedMsg{generation: m.op.gen})
	if cmd == nil || m.op.kind != opLogout {
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

func TestQueuedLogoutFailurePostsOneNotice(t *testing.T) {
	want := errors.New("credential store unavailable")
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}), Config{
		Logout: func(context.Context) (bool, error, error) {
			return true, nil, want
		},
	})
	_, cancel := context.WithCancel(context.Background())
	m.op = operation{kind: opTurn, events: make(chan agent.Event), cancel: cancel}
	_, _ = m.dispatchCommand("new", "")
	_, _ = m.dispatchCommand("logout", "")
	if len(m.notices) != 0 {
		t.Fatalf("queued operation posted transcript progress: %+v", m.notices)
	}

	_, cmd := m.handleTurnClosed(turnClosedMsg{generation: m.op.gen})
	if cmd == nil || m.op.kind != opLogout || m.promptPlaceholder() != "Logging out…" {
		t.Fatal("logout did not continue after the turn drained")
	}
	_, _ = m.Update(cmd())
	if len(m.notices) != 1 || !errors.Is(m.notices[0].Notice.Err, want) || m.promptPlaceholder() != "Ask Bits…" {
		t.Fatalf("logout failure notice or placeholder: notices=%+v placeholder=%q", m.notices, m.promptPlaceholder())
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
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for range 20 {
		m.postNotice(notice(chat.NoticeInfo, nil, "Earlier message"))
	}
	m.list.ScrollToTop()

	_, cmd := m.dispatchCommand("logout", "")
	if m.list.Following() {
		t.Fatal("starting logout moved the transcript without a result")
	}
	msg := cmd()
	_, _ = m.Update(msg)
	if !m.list.Following() || !strings.Contains(ansi.Strip(m.list.Render()), "could not log out") {
		t.Fatalf("logout result is outside the viewport:\n%s", ansi.Strip(m.list.Render()))
	}
	if m.engine != engine {
		t.Fatal("local deletion failure invalidated the usable engine")
	}
	if m.op.kind == opLogout {
		t.Fatal("logout remained active after failure")
	}
	if !errors.Is(latestNotice(m).Err, want) || latestNotice(m).Level != chat.NoticeError {
		t.Fatalf("failure notice = %+v", latestNotice(m))
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
	_, _ = m.dispatchCommand("logout", "")
	if latestNotice(m).Empty() {
		t.Fatal("repeated logout did not report in-progress state")
	}
	_ = first()
	if calls != 1 {
		t.Fatalf("logout calls = %d, want 1", calls)
	}
}

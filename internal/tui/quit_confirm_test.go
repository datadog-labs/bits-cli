package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

var ctrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

func newSizedChatModel(t *testing.T) *Model {
	t.Helper()
	m := newModelWithSpy(t)
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 40})
	return m
}

func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestCtrlCWithDraftClearsAndStaysOpen(t *testing.T) {
	m := newSizedChatModel(t)
	m.editor.SetValue("hello draft")

	_, cmd := m.Update(ctrlC)
	if isQuitCmd(cmd) {
		t.Fatal("ctrl+c with a draft must not quit")
	}
	if m.editor.Value() != "" {
		t.Fatalf("draft = %q, want cleared", m.editor.Value())
	}
	if m.quitConfirm.armed {
		t.Fatal("clearing a draft must not arm exit")
	}
}

func TestCtrlCOnEmptyPromptNeedsSecondPress(t *testing.T) {
	m := newSizedChatModel(t)

	_, _ = m.Update(ctrlC)
	if !m.quitConfirm.armed {
		t.Fatal("first ctrl+c on an empty prompt did not arm exit")
	}
	if !strings.Contains(m.chatFooter(), quitConfirmHint) {
		t.Fatalf("footer = %q, want exit hint", m.chatFooter())
	}

	_, cmd := m.Update(ctrlC)
	if !isQuitCmd(cmd) {
		t.Fatal("second ctrl+c within the window did not quit")
	}
}

func TestCtrlCConfirmExpires(t *testing.T) {
	m := newSizedChatModel(t)

	_, _ = m.Update(ctrlC)
	_, _ = m.Update(quitConfirmExpiredMsg{token: m.quitConfirm.token})
	if m.quitConfirm.armed {
		t.Fatal("expiry did not disarm exit")
	}
	if strings.Contains(m.chatFooter(), quitConfirmHint) {
		t.Fatal("hint still shown after expiry")
	}
	_, _ = m.Update(ctrlC)
	if !m.quitConfirm.armed {
		t.Fatal("ctrl+c after expiry must arm again, not quit")
	}
}

func TestCtrlCStaleExpiryKeepsNewerWindow(t *testing.T) {
	m := newSizedChatModel(t)

	_, _ = m.Update(ctrlC)
	stale := m.quitConfirm.token
	_, _ = m.Update(tea.KeyPressMsg{Code: 'x'})
	m.editor.Reset()
	_, _ = m.Update(ctrlC)
	_, _ = m.Update(quitConfirmExpiredMsg{token: stale})
	if !m.quitConfirm.armed {
		t.Fatal("a stale expiry cut the newer exit window short")
	}
}

func TestCtrlCOtherKeyDisarms(t *testing.T) {
	m := newSizedChatModel(t)

	_, _ = m.Update(ctrlC)
	_, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.quitConfirm.armed {
		t.Fatal("typing did not abandon the pending exit")
	}
	_, cmd := m.Update(ctrlC)
	if isQuitCmd(cmd) {
		t.Fatal("ctrl+c after typing must clear the draft, not quit")
	}
}

func TestCtrlCDoesNotInterruptRunningTurn(t *testing.T) {
	m := newSizedChatModel(t)
	cancelled := false
	m.op = operation{kind: opTurn, run: 1, cancel: func() { cancelled = true }}
	m.editor.SetValue("queued thought")

	_, _ = m.Update(ctrlC)
	_, _ = m.Update(ctrlC)
	if cancelled || !m.op.busy() {
		t.Fatal("ctrl+c interrupted the running turn")
	}
	if !m.quitConfirm.armed {
		t.Fatal("ctrl+c during a turn did not arm exit")
	}
}

func TestCtrlCClearingDraftRebuildsLayout(t *testing.T) {
	m := newSizedChatModel(t)
	empty := m.frame.transcript.Dy()
	m.Update(tea.PasteMsg{Content: "one\ntwo\nthree\nfour"})
	if m.frame.transcript.Dy() >= empty {
		t.Fatalf("multiline draft did not shrink the transcript: %d >= %d", m.frame.transcript.Dy(), empty)
	}

	_, _ = m.Update(ctrlC)
	if m.frame.transcript.Dy() != empty {
		t.Fatalf("transcript height = %d after clearing, want %d", m.frame.transcript.Dy(), empty)
	}
}

func TestCtrlCHintShownWhenTerminalTooSmall(t *testing.T) {
	m := newModelWithSpy(t)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 3})
	if !m.frame.tooSmall {
		t.Fatal("expected the resize view")
	}

	_, _ = m.Update(ctrlC)
	if content := m.View().Content; !strings.Contains(content, quitConfirmHint) {
		t.Fatalf("resize view = %q, want exit hint", content)
	}
	_, cmd := m.Update(ctrlC)
	if !isQuitCmd(cmd) {
		t.Fatal("second ctrl+c in the resize view did not quit")
	}
}

func TestCtrlCClearingPastedDraftDisarms(t *testing.T) {
	m := newSizedChatModel(t)

	_, _ = m.Update(ctrlC)
	_, _ = m.Update(tea.PasteMsg{Content: "pasted"})
	_, _ = m.Update(ctrlC)
	if m.quitConfirm.armed {
		t.Fatal("clearing a pasted draft left exit armed")
	}
	_, cmd := m.Update(ctrlC)
	if isQuitCmd(cmd) {
		t.Fatal("ctrl+c after clearing a pasted draft quit without a fresh confirmation")
	}
}

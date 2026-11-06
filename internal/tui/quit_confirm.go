package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// quitConfirmWindow is how long an empty-composer ctrl+c waits for the second
// press that exits.
const quitConfirmWindow = 1500 * time.Millisecond

// quitConfirmHint replaces the chat footer while a second ctrl+c would exit.
const quitConfirmHint = "Press Ctrl+C again to exit"

// quitConfirmExpiredMsg disarms the pending exit. token ties it to the press
// that armed it, so an older timer cannot cut a newer window short.
type quitConfirmExpiredMsg struct{ token uint64 }

// quitConfirm tracks the first ctrl+c of an exit.
type quitConfirm struct {
	armed bool
	token uint64
}

// handleChatCtrlC keeps ctrl+c from discarding work in chat:
//
//   - a visible draft (text or attachments) is cleared, and the app stays open;
//   - an empty composer arms exit, and a second press within
//     quitConfirmWindow quits.
//
// It never cancels the running turn: esc interrupts a turn, and only the
// confirmed exit stops it.
func (m *Model) handleChatCtrlC() (tea.Model, tea.Cmd) {
	if m.composerHasDraft() {
		m.quitConfirm.armed = false
		m.editor.Reset()
		return m, m.stopCompletionSearches()
	}
	if m.quitConfirm.armed {
		return m.quit()
	}
	m.quitConfirm.armed = true
	m.quitConfirm.token++
	token := m.quitConfirm.token
	return m, tea.Tick(quitConfirmWindow, func(time.Time) tea.Msg {
		return quitConfirmExpiredMsg{token: token}
	})
}

// composerHasDraft reports whether the composer shows input that ctrl+c would
// clear. A draft hidden behind a replacing prompt or the resize hint is kept.
func (m *Model) composerHasDraft() bool {
	if m.editor == nil || m.frame.replaced || m.frame.tooSmall {
		return false
	}
	return m.editor.Value() != "" || len(m.editor.Attachments()) > 0
}

func (m *Model) expireQuitConfirm(msg quitConfirmExpiredMsg) {
	if msg.token == m.quitConfirm.token {
		m.quitConfirm.armed = false
	}
}

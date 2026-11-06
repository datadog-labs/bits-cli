package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const quitConfirmWindow = 1500 * time.Millisecond

const quitConfirmHint = "Press Ctrl+C again to exit"

type quitConfirmExpiredMsg struct{ token uint64 }

type quitConfirm struct {
	armed bool
	token uint64
}

func (m *Model) clearDraftOrArmQuit() tea.Cmd {
	if m.composerHasDraft() {
		m.quitConfirm.armed = false
		m.editor.Reset()
		return m.stopCompletionSearches()
	}
	m.quitConfirm.armed = true
	m.quitConfirm.token++
	token := m.quitConfirm.token
	return tea.Tick(quitConfirmWindow, func(time.Time) tea.Msg {
		return quitConfirmExpiredMsg{token: token}
	})
}

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

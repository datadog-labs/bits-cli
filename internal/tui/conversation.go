package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// requestNewConversation defers the reset until the current engine channel is
// closed. cancelRemote makes repeated /new or /clear submissions idempotent and
// prevents cleanup from invoking cancellation a second time.
func (m *Model) requestNewConversation() tea.Cmd {
	m.pendingNew = true
	m.cancelRemote()
	return m.showNotice(notice(chat.NoticeInfo, nil,
		"Cancelling the current operation before starting a new conversation…"), 0)
}

// startNewConversation resets only conversation-scoped state. It deliberately
// leaves the editor object (and therefore local input history), styles, backend
// client, and process-wide configuration intact.
func (m *Model) startNewConversation() tea.Cmd {
	if err := m.engine.NewConversation(); err != nil {
		return m.showNotice(noticeForError("could not start a new conversation", err), 0)
	}

	// A reset is a new event identity domain even though active work was drained.
	// This makes any delayed Bubble Tea message from the prior domain harmless.
	m.turnGen++
	m.blocks = nil
	m.list.Reset()
	m.convID = ""
	m.usage = nil
	m.chatPhase = chat.PhaseIdle
	m.pendingNew = false
	m.editor.Reset()
	m.clearNotice()
	m.setMode(ModeChat)
	m.list.ScrollToBottom()
	return m.editor.Focus()
}

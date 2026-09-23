package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

const logoutTimeout = 60 * time.Second

// requestLogout waits for active Assistant work to stop before credential
// deletion. This ordering prevents an authenticated client from continuing a
// turn after its durable session has been removed.
func (m *Model) requestLogout() tea.Cmd {
	m.pendingLogout = true
	closeFileSearch := m.stopCompletionSearches()
	m.editor.CloseMenu()
	m.cancelRemote()
	return batchCommands(
		closeFileSearch,
		m.showNotice(notice(chat.NoticeInfo, nil,
			"Cancelling the current operation before logging out..."), 0),
	)
}

func (m *Model) startLogout() tea.Cmd {
	closeFileSearch := m.stopCompletionSearches()
	if m.logout == nil {
		return batchCommands(
			closeFileSearch,
			m.showNotice(notice(chat.NoticeError, nil, "Logout is unavailable."), 0),
		)
	}
	m.logoutGeneration++
	generation := m.logoutGeneration
	ctx, cancel := context.WithTimeout(context.Background(), logoutTimeout)
	m.logoutCancel = cancel
	m.logoutRunning = true
	m.editor.CloseMenu()
	m.clearNotice()
	logout := m.logout
	return func() tea.Msg {
		if closeFileSearch != nil {
			_ = closeFileSearch()
		}
		_, _, err := logout(ctx)
		return logoutResultMsg{
			generation: generation,
			err:        err,
		}
	}
}

func (m *Model) applyLogoutResult(msg logoutResultMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.logoutGeneration || !m.logoutRunning {
		return m, nil
	}
	if m.logoutCancel != nil {
		m.logoutCancel()
		m.logoutCancel = nil
	}
	m.logoutRunning = false
	if msg.err != nil {
		return m, m.showNotice(noticeForError("could not log out", msg.err), 0)
	}

	// Discard authenticated collaborators and invalidate queued search results.
	closeFileSearch := m.stopCompletionSearches()
	m.entitySearcher = nil
	m.engine = nil
	m.convID = ""
	m.turnEvents = nil
	m.cancelTurn = nil
	m.pendingApprovals = nil
	m.approvalPanel.ResetScroll()
	m.loggedOut = true
	if closeFileSearch == nil {
		return m, tea.Quit
	}
	return m, func() tea.Msg {
		_ = closeFileSearch()
		return tea.Quit()
	}
}

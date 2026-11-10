package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

const logoutTimeout = 60 * time.Second

// requestLogout waits for active Assistant work to stop before credential
// deletion. This ordering prevents an authenticated client from continuing a
// turn after its durable session has been removed.
func (m *Model) requestLogout() tea.Cmd {
	closeFileSearch := m.stopCompletionSearches()
	m.editor.CloseMenu()
	m.after(thenLogout)
	return closeFileSearch
}

func (m *Model) startLogout() tea.Cmd {
	closeFileSearch := m.stopCompletionSearches()
	if m.logout == nil {
		m.postNotice(notice(chat.NoticeError, nil, "Logout is unavailable."))
		m.list.ScrollToBottom()
		return closeFileSearch
	}
	ctx, cancel := context.WithTimeout(context.Background(), logoutTimeout)
	if !m.begin(opLogout, cancel) {
		return closeFileSearch
	}
	closeBackground := m.stopBackgroundTasks()
	generation := m.op.gen
	m.editor.CloseMenu()
	logout := m.logout
	return func() tea.Msg {
		if closeFileSearch != nil {
			_ = closeFileSearch()
		}
		if closeBackground != nil {
			_ = closeBackground()
		}
		_, _, err := logout(ctx)
		return logoutResultMsg{
			generation: generation,
			err:        err,
		}
	}
}

func (m *Model) applyLogoutResult(msg logoutResultMsg) (tea.Model, tea.Cmd) {
	if !m.op.matches(opLogout, msg.generation) {
		return m, nil
	}
	m.endOperation()
	if msg.err != nil {
		// A canceled discovery may not have produced a menu. Reconcile it again
		// only when logout fails and the authenticated session remains usable.
		m.skillMenu.started = false
		m.postNotice(noticeForError("could not log out", msg.err))
		m.list.ScrollToBottom()
		return m, nil
	}

	// Discard authenticated collaborators and invalidate queued search results.
	m.skillMenu.task.stop()
	m.skillMenu.skills = nil
	m.editor.SetCommands(commandCompletionSpecs())
	closeFileSearch := m.stopCompletionSearches()
	m.entitySearcher = nil
	m.engine = nil
	m.state.ConversationID = ""
	m.clearAsks()
	m.loggedOut = true
	if closeFileSearch == nil {
		return m, m.quitWith(tea.Quit)
	}
	return m, m.quitWith(func() tea.Msg {
		_ = closeFileSearch()
		return tea.Quit()
	})
}

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
	m.begin(opLogout, nil, cancel)
	generation := m.op.gen
	m.editor.CloseMenu()
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
	if m.op.kind != opLogout || msg.generation != m.op.gen {
		return m, nil
	}
	m.op.cancel()
	m.op = operation{gen: m.op.gen}
	if msg.err != nil {
		m.postNotice(noticeForError("could not log out", msg.err))
		m.list.ScrollToBottom()
		return m, nil
	}

	// Discard authenticated collaborators and invalidate queued search results.
	m.localSkillsTask.stop()
	m.localSkills = nil
	m.editor.SetCommands(commandCompletionSpecs())
	closeFileSearch := m.stopCompletionSearches()
	m.entitySearcher = nil
	m.engine = nil
	m.convID = ""
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

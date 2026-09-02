package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

const logoutTimeout = 60 * time.Second

// LogoutResult is the non-secret outcome of a completed native logout.
type LogoutResult struct {
	HadSession bool
	RevokeErr  error
}

// requestLogout waits for active Assistant work to stop before credential
// deletion. This ordering prevents an authenticated client from continuing a
// turn after its durable session has been removed.
func (m *Model) requestLogout() tea.Cmd {
	m.pendingLogout = true
	m.cancelRemote()
	return m.showNotice(notice(chat.NoticeInfo, nil,
		"Cancelling the current operation before logging out..."), 0)
}

func (m *Model) startLogout() tea.Cmd {
	if m.logout == nil {
		return m.showNotice(notice(chat.NoticeError, nil, "Logout is unavailable."), 0)
	}
	m.logoutGeneration++
	generation := m.logoutGeneration
	ctx, cancel := context.WithTimeout(context.Background(), logoutTimeout)
	m.logoutCancel = cancel
	m.logoutRunning = true
	m.clearNotice()
	logout := m.logout
	return func() tea.Msg {
		hadSession, revokeErr, err := logout(ctx)
		return logoutResultMsg{
			generation: generation,
			result: LogoutResult{
				HadSession: hadSession,
				RevokeErr:  revokeErr,
			},
			err: err,
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

	// Local deletion is authoritative. Drop every reference that can issue an
	// authenticated Assistant request before leaving the program.
	m.engine = nil
	m.convID = ""
	m.turnEvents = nil
	m.cancelTurn = nil
	m.pendingApprovals = nil
	m.logoutResult = &msg.result
	return m, tea.Quit
}

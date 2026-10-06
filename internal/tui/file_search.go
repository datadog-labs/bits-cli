package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/tui/editor"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

const fileSearchLimit = 5

type fileSearchSnapshotMsg struct {
	session  *workspace.FileSearchSession
	snapshot workspace.FileSearchSnapshot
	ok       bool
}

type fileSearchClosedMsg struct{}

// syncFileSearch reconciles the editor's active non-empty @ query with one
// session-scoped workspace index. Query changes reuse the live session and only
// update its matcher.
func (m *Model) syncFileSearch() tea.Cmd {
	query, active := m.editor.ActiveEntityQuery()
	if m.searchesBlocked() || !active || query == "" {
		return m.stopFileSearch()
	}
	if m.workspace == nil {
		if query != m.fileSearchQuery {
			m.fileSearchQuery = query
			m.editor.SetFileResults(query, editor.FileError, nil)
		}
		return nil
	}
	if m.fileSearchSession != nil && query == m.fileSearchQuery {
		return nil
	}

	if m.fileSearchSession == nil {
		m.fileSearchSession = m.workspace.NewFileSearchSession(
			context.Background(),
			workspace.FileSearchOptions{Limit: fileSearchLimit},
		)
	}
	m.fileSearchQuery = query
	m.fileSearchGeneration = m.fileSearchSession.UpdateQuery(query)
	m.editor.SetFileResults(query, editor.FileIndexing, nil)
	return m.waitForFileSearch()
}

func (m *Model) waitForFileSearch() tea.Cmd {
	if m.fileSearchSession == nil || m.fileSearchWaiting {
		return nil
	}
	session := m.fileSearchSession
	m.fileSearchWaiting = true
	return func() tea.Msg {
		snapshot, ok := <-session.Updates()
		return fileSearchSnapshotMsg{session: session, snapshot: snapshot, ok: ok}
	}
}

func (m *Model) applyFileSearchSnapshot(msg fileSearchSnapshotMsg) tea.Cmd {
	if msg.session != m.fileSearchSession {
		return nil
	}
	m.fileSearchWaiting = false
	if !msg.ok {
		m.fileSearchSession = nil
		m.editor.SetFileResults(m.fileSearchQuery, editor.FileError, nil)
		return nil
	}
	if msg.snapshot.Generation != m.fileSearchGeneration || msg.snapshot.Query != m.fileSearchQuery {
		return m.waitForFileSearch()
	}

	state := editor.FileIndexing
	if msg.snapshot.Err != nil {
		state = editor.FileError
	} else if msg.snapshot.Complete {
		state = editor.FileReady
	}
	m.editor.SetFileResults(msg.snapshot.Query, state, fileCandidates(msg.snapshot.Results))
	return m.waitForFileSearch()
}

// stopFileSearch immediately invalidates the active session and returns its
// potentially blocking join as a Bubble Tea command.
func (m *Model) stopFileSearch() tea.Cmd {
	session := m.fileSearchSession
	m.fileSearchSession = nil
	m.fileSearchQuery = ""
	m.fileSearchGeneration = 0
	m.fileSearchWaiting = false
	if m.editor != nil {
		m.editor.SetFileResults("", editor.FileIdle, nil)
	}
	if session == nil {
		return nil
	}
	return func() tea.Msg {
		session.Close()
		return fileSearchClosedMsg{}
	}
}

// searchesBlocked stops completion searches from reaching an engine that is
// being, or has been, logged out.
func (m *Model) searchesBlocked() bool {
	return m.op.loggingOut() || m.loggedOut
}

func (m *Model) syncCompletionSearches() tea.Cmd {
	return tea.Batch(m.syncEntitySearch(), m.syncFileSearch())
}

func (m *Model) stopCompletionSearches() tea.Cmd {
	m.stopEntitySearch()
	return m.stopFileSearch()
}

func fileCandidates(results []workspace.FileSearchResult) []editor.Candidate {
	items := make([]editor.Candidate, 0, len(results))
	for _, result := range results {
		items = append(items, editor.Candidate{
			Kind:   editor.CandidateFile,
			ID:     result.Path,
			Label:  "+ " + escape.SingleLine(result.Path),
			Insert: "@" + escape.Inline(result.Path),
		})
	}
	return items
}

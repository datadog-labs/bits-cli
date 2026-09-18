package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/editor"
	"github.com/DataDog/bits-cli/internal/workspace"
)

func newModelWithFileWorkspace(t *testing.T, logout LogoutFunc) (*Model, *workspace.Workspace) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"go.mod", "README.md", "internal/tui/model.go"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	m := New(
		agent.New(&acceptingBackend{requests: make(chan assistant.SendOptions, 1)}, assistant.SendOptions{}),
		Config{Workspace: ws, Logout: logout},
	)
	t.Cleanup(func() {
		if closeFileSearch := m.stopFileSearch(); closeFileSearch != nil {
			_ = closeFileSearch()
		}
		if err := ws.Close(); err != nil {
			t.Error(err)
		}
	})
	return m, ws
}

func TestFileSearchSessionLifecycleReusesIndexUntilQueryClears(t *testing.T) {
	m, _ := newModelWithFileWorkspace(t, nil)
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@go"})
	if wait := m.syncFileSearch(); wait == nil {
		t.Fatal("first non-empty query did not start a session waiter")
	}
	first := m.fileSearchSession
	firstGeneration := m.fileSearchGeneration
	if first == nil {
		t.Fatal("first non-empty query did not create a session")
	}

	m.editor.Update(tea.PasteMsg{Content: "m"})
	if wait := m.syncFileSearch(); wait != nil {
		t.Fatal("query update armed a second concurrent waiter")
	}
	if m.fileSearchSession != first {
		t.Fatal("query update recreated the file-search session")
	}
	if m.fileSearchGeneration <= firstGeneration || m.fileSearchQuery != "gom" {
		t.Fatalf("updated query state = (%q, %d), want gom after generation %d", m.fileSearchQuery, m.fileSearchGeneration, firstGeneration)
	}

	m.editor.Reset()
	closeFirst := m.syncFileSearch()
	if closeFirst == nil || m.fileSearchSession != nil {
		t.Fatal("clearing the query did not detach the active session")
	}
	_ = closeFirst()

	m.editor.Update(tea.PasteMsg{Content: "@read"})
	if wait := m.syncFileSearch(); wait == nil || m.fileSearchSession == nil {
		t.Fatal("later non-empty interaction did not create a session")
	}
	if m.fileSearchSession == first {
		t.Fatal("later non-empty interaction reused the discarded session")
	}
}

func TestFileSearchRejectsStaleSnapshotsAndRendersPartialThenFinal(t *testing.T) {
	m, _ := newModelWithFileWorkspace(t, nil)
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "@go"})
	_ = m.syncFileSearch()
	session := m.fileSearchSession
	oldGeneration := m.fileSearchGeneration

	m.editor.Update(tea.PasteMsg{Content: "m"})
	_ = m.syncFileSearch()
	generation := m.fileSearchGeneration
	m.applyFileSearchSnapshot(fileSearchSnapshotMsg{
		session: session,
		ok:      true,
		snapshot: workspace.FileSearchSnapshot{
			Query: "go", Generation: oldGeneration, Complete: true,
			Results: []workspace.FileSearchResult{{Path: "stale.go"}},
		},
	})
	if m.editor.MenuHasCandidates() || m.editor.MenuView() == "" || !strings.Contains(m.editor.MenuView(), "Indexing files") {
		t.Fatalf("stale snapshot changed the indexing menu: %q", m.editor.MenuView())
	}

	m.applyFileSearchSnapshot(fileSearchSnapshotMsg{
		session: session,
		ok:      true,
		snapshot: workspace.FileSearchSnapshot{
			Query: "gom", Generation: generation, Complete: false,
			Results: []workspace.FileSearchResult{{Path: "go.mod"}},
		},
	})
	partial := m.editor.MenuView()
	if !strings.Contains(partial, "+ go.mod") || !strings.Contains(partial, "Indexing files") {
		t.Fatalf("partial snapshot menu = %q", partial)
	}

	m.applyFileSearchSnapshot(fileSearchSnapshotMsg{
		session: session,
		ok:      true,
		snapshot: workspace.FileSearchSnapshot{
			Query: "gom", Generation: generation, Complete: true,
			Results: []workspace.FileSearchResult{{Path: "go.mod"}},
		},
	})
	final := m.editor.MenuView()
	if !strings.Contains(final, "+ go.mod") || strings.Contains(final, "Indexing files") {
		t.Fatalf("final snapshot menu = %q", final)
	}
}

func TestFileSearchCleanupOnResetLogoutAndQuit(t *testing.T) {
	start := func(t *testing.T, logout LogoutFunc) (*Model, *workspace.FileSearchSession) {
		t.Helper()
		m, _ := newModelWithFileWorkspace(t, logout)
		m.editor.Focus()
		m.editor.Update(tea.PasteMsg{Content: "@go"})
		_ = m.syncFileSearch()
		return m, m.fileSearchSession
	}
	assertClosed := func(t *testing.T, session *workspace.FileSearchSession) {
		t.Helper()
		for range session.Updates() {
		}
	}

	t.Run("conversation reset", func(t *testing.T) {
		m, session := start(t, nil)
		closeFileSearch := m.startNewConversation()
		if m.fileSearchSession != nil || closeFileSearch == nil {
			t.Fatal("conversation reset retained the file-search session")
		}
		_ = closeFileSearch()
		assertClosed(t, session)
	})

	t.Run("logout", func(t *testing.T) {
		m, session := start(t, func(context.Context) (bool, error, error) { return true, nil, nil })
		logout := m.startLogout()
		if m.fileSearchSession != nil || logout == nil {
			t.Fatal("logout retained the file-search session")
		}
		msg, ok := logout().(logoutResultMsg)
		if !ok || msg.err != nil {
			t.Fatalf("logout result = %#v", msg)
		}
		assertClosed(t, session)
		_, quit := m.applyLogoutResult(msg)
		if quit == nil {
			t.Fatal("successful logout did not quit")
		}
	})

	t.Run("quit", func(t *testing.T) {
		m, session := start(t, nil)
		_, quit := m.quit()
		if m.fileSearchSession != nil || quit == nil {
			t.Fatal("quit retained the file-search session")
		}
		if _, ok := quit().(tea.QuitMsg); !ok {
			t.Fatal("quit did not return a Bubble Tea quit message")
		}
		assertClosed(t, session)
	})
}

func TestFileCandidatesEscapeDisplayAndInsertion(t *testing.T) {
	items := fileCandidates([]workspace.FileSearchResult{
		{Path: "unsafe\x1b[31m.go"},
		{Path: "internal/tui/"},
	})
	if len(items) != 2 || strings.Contains(items[0].Label, "\x1b") || items[0].Insert != "@unsafe␛[31m.go" {
		t.Fatalf("file candidate = %#v", items)
	}
	if items[0].Kind != editor.CandidateFile {
		t.Fatalf("candidate kind = %q, want file", items[0].Kind)
	}
	if items[1].Label != "+ internal/tui/" || items[1].Insert != "@internal/tui/" {
		t.Fatalf("directory candidate = %#v", items[1])
	}
}

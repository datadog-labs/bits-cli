package status

import (
	"strings"
	"testing"

	"charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestModelRendersTruthfulRuntimeAndWorkspaceSnapshot(t *testing.T) {
	m := New(96, 40, styles.Default(true))
	m.Open(Runtime{
		Site:                "https://api.us3.datadoghq.com",
		AuthenticationMode:  "oauth",
		AuthenticationState: "authenticated",
		ConversationID:      "conversation-123",
		Profile:             "cli",
		ApprovalMode:        "gated",
		Phase:               "streaming",
		Connectivity:        ConnectivityConnected,
		Usage:               &assistant.Usage{TokensUsed: 3200, MaxTokens: 16000},
	})
	m.SetEnvironment(Environment{
		WorkingDirectory: "/work/bits-cli",
		Repository: Repository{
			State:  RepositoryPresent,
			Root:   "/work/bits-cli",
			Name:   "bits-cli",
			Branch: "main",
			Commit: "0123456789abcdef0123456789abcdef01234567",
			Changes: Changes{
				Known:     true,
				Staged:    true,
				Untracked: true,
			},
		},
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Bits status", "esc ×",
		"Datadog", "Site", "https://api.us3.datadoghq.com", "Authentication", "oauth · authenticated",
		"User / org", "unavailable — authenticated identity is not exposed by this client",
		"Assistant", "Conversation", "conversation-123", "Profile", "cli",
		"Model", "default",
		"Approval", "gated", "Turn", "streaming", "Connectivity", "connected", "Context tokens", "3,200 / 16,000",
		"Workspace", "Directory", "/work/bits-cli", "Repository", "bits-cli · /work/bits-cli",
		"Branch", "main", "Commit", "0123456789abcdef0123456789abcdef01234567",
		"Working tree", "staged, untracked",
		"↑/↓ scroll", "pgup/pgdown page",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("status view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "collecting") {
		t.Fatalf("loaded status still shows pending values:\n%s", view)
	}
}

func TestModelLabelsUnavailableNonGitAndUnauthenticatedValues(t *testing.T) {
	m := New(88, 40, styles.Default(false))
	m.Open(Runtime{
		AuthenticationMode:  "none (fake backend)",
		AuthenticationState: "unauthenticated",
		Phase:               "idle",
		Connectivity:        ConnectivityNotChecked,
	})
	m.SetEnvironment(Environment{
		WorkingDirectory: "/tmp/scratch",
		Repository:       Repository{State: RepositoryAbsent},
	})

	view := ansi.Strip(m.View())
	for _, want := range []string{
		"Site", "unavailable — no remote Datadog site is configured",
		"Authentication", "none (fake backend) · unauthenticated",
		"Conversation", "unavailable — no conversation has been created",
		"Context tokens", "unavailable — no usage has been reported",
		"Repository", "not a Git repository",
		"Branch", "not applicable", "Commit", "not applicable", "Working tree", "not applicable",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("partial status missing %q:\n%s", want, view)
		}
	}
}

func TestModelScrollsInsideSharedPanelAndEscapeCloses(t *testing.T) {
	m := New(72, 16, styles.Default(true))
	m.Open(Runtime{Phase: "idle", Connectivity: ConnectivityNotChecked})
	m.SetEnvironment(Environment{Repository: Repository{State: RepositoryUnavailable}})

	before := ansi.Strip(m.View())
	for range 30 {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	after := ansi.Strip(m.View())
	if before == after || !strings.Contains(after, "Working tree") {
		t.Fatalf("status body did not scroll to workspace:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("escape did not return a close command")
	}
	if _, ok := cmd().(ClosedMsg); !ok {
		t.Fatalf("escape command returned %T, want ClosedMsg", cmd())
	}
}

func TestModelViewStaysWithinTerminalBounds(t *testing.T) {
	for _, size := range []struct{ width, height int }{{96, 24}, {36, 8}, {12, 2}} {
		m := New(size.width, size.height, styles.Default(true))
		m.Open(Runtime{Phase: "idle", Connectivity: ConnectivityNotChecked})
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.height {
			t.Fatalf("%dx%d view height = %d", size.width, size.height, len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got > size.width {
				t.Fatalf("%dx%d line %d width = %d", size.width, size.height, i+1, got)
			}
		}
	}
}

func TestNarrowValuesHangIndentUnderValueColumn(t *testing.T) {
	m := New(72, 40, styles.Default(true))
	m.Open(Runtime{Phase: "idle", Connectivity: ConnectivityNotChecked})
	m.SetEnvironment(Environment{Repository: Repository{State: RepositoryAbsent}})
	lines := strings.Split(ansi.Strip(m.View()), "\n")

	valueColumn := -1
	continuationColumn := -1
	for _, line := range lines {
		if strings.Contains(line, "User / org") {
			valueColumn = strings.Index(line, "unavailable")
		}
		if strings.Contains(line, "exposed by this client") {
			continuationColumn = strings.Index(line, "exposed")
		}
	}
	if valueColumn < 0 || continuationColumn < 0 {
		t.Fatalf("could not find wrapped identity row:\n%s", strings.Join(lines, "\n"))
	}
	if continuationColumn != valueColumn {
		t.Fatalf("continuation column = %d, want value column %d:\n%s", continuationColumn, valueColumn, strings.Join(lines, "\n"))
	}
}

package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/workspace"
)

func TestChatFooterRendersBelowEditorWithoutGrowingView(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bits-cli-worktree")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	m := New(
		agent.New(&spyBackend{t: t}, assistant.SendOptions{}),
		Config{Workspace: ws},
	)
	_ = m.editor.Focus()
	m.usage = &assistant.Usage{TokensUsed: 330_000}
	m.resize(120, 24)

	view := m.chatView()
	if got := lipgloss.Height(view); got != m.height {
		t.Fatalf("chat height = %d, want %d", got, m.height)
	}
	lines := strings.Split(ansi.Strip(view), "\n")
	footer := lines[len(lines)-1]
	if !strings.Contains(footer, "bits-cli-worktree") || !strings.Contains(footer, " · 330K used") {
		t.Fatalf("footer = %q", footer)
	}
	if editor := ansi.Strip(m.editor.View()); strings.LastIndex(ansi.Strip(view), editor) > strings.LastIndex(ansi.Strip(view), footer) {
		t.Fatalf("footer was not rendered below editor:\n%s", ansi.Strip(view))
	}
}

func TestChatFooterTextIsSafeAndWidthBounded(t *testing.T) {
	usage := &assistant.Usage{TokensUsed: 330_000}
	for _, width := range []int{1, 8, 24, 80} {
		text := chatFooterText("/tmp/private\x1b[31m\nworkspace", usage, width)
		if strings.ContainsAny(text, "\x1b\n\r") {
			t.Fatalf("width %d footer retained terminal controls: %q", width, text)
		}
		if got := ansi.StringWidth(text); got > width {
			t.Fatalf("width %d footer rendered %d cells: %q", width, got, text)
		}
	}
	if got := chatFooterText("/tmp/workspace", usage, 24); !strings.HasSuffix(got, " · 330K used") {
		t.Fatalf("footer did not preserve usage suffix: %q", got)
	}
}

func TestCompactTokenCount(t *testing.T) {
	for value, want := range map[int]string{
		999:       "999",
		1_200:     "1.2K",
		16_000:    "16K",
		330_000:   "330K",
		1_500_000: "1.5M",
	} {
		if got := compactTokenCount(value); got != want {
			t.Errorf("compactTokenCount(%d) = %q, want %q", value, got, want)
		}
	}
}

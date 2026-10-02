package tui

import (
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/workspace"
)

// A frame's surfaces tile the screen top to bottom, View draws each one on the
// rows the frame gives it, and a frame that cannot be usably drawn is marked
// too small instead.
func TestLayoutTilesTheScreen(t *testing.T) {
	approval := func(m *Model) { m.pendingApprovals = []agent.Block{animToolBlock(agent.ToolAwaitingApproval)} }
	draft := func(m *Model) { setConversationInput(m, strings.Repeat("draft\n", 12)) }
	toolUI := func(m *Model) {
		component, _ := chat.NewToolInteraction(agent.ToolCall{Name: spec.AskUserQuestion, Input: questionInput})
		m.activeToolUI = &toolUISession{component: component}
	}
	for _, tc := range []struct {
		name          string
		width, height int
		setup         []func(*Model)
		tooSmall      bool
	}{
		{"composer", 80, 24, nil, false},
		{"tall draft under the header", 80, 22, []func(*Model){draft}, false},
		{"approval", 80, minimumApprovalHeight, []func(*Model){approval}, false},
		{"tool UI", 80, 24, []func(*Model){toolUI}, false},
		{"narrow chat", minimumChatWidth - 1, 24, nil, true},
		{"short approval", 80, minimumApprovalHeight - 1, []func(*Model){approval}, true},
		{"approval squeezed by a draft", 80, minimumApprovalHeight, []func(*Model){approval, draft}, true},
		{"small tool UI", 30, 10, []func(*Model){toolUI}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newShell()
			m.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
			for _, setup := range tc.setup {
				setup(m)
			}
			m.relayout()
			f := m.frame
			if f.tooSmall != tc.tooSmall {
				t.Fatalf("tooSmall = %t, want %t", f.tooSmall, tc.tooSmall)
			}
			if f.tooSmall {
				return
			}

			rects := slices.DeleteFunc([]image.Rectangle{f.transcript, f.dock, f.composerGap, f.editor, f.footer}, image.Rectangle.Empty)
			slices.SortFunc(rects, func(a, b image.Rectangle) int { return a.Min.Y - b.Min.Y })
			for i, r := range rects {
				if (i == 0 && r != f.transcript) || (i > 0 && r.Min.Y != rects[i-1].Max.Y) || r.Dx() != tc.width {
					t.Fatalf("surfaces %v do not tile from the transcript down", rects)
				}
			}
			view := strings.Split(ansi.Strip(m.View().Content), "\n")
			if len(view) != tc.height || rects[len(rects)-1].Max.Y != tc.height {
				t.Fatalf("view is %d rows and surfaces end at %d, want %d", len(view), rects[len(rects)-1].Max.Y, tc.height)
			}
			if f.composerGap.Dy() != chatComposerGapHeight || strings.TrimSpace(view[f.composerGap.Min.Y]) != "" {
				t.Fatalf("composer gap is not one empty row: rect=%v row=%q", f.composerGap, view[f.composerGap.Min.Y])
			}
			firstRow := func(s string) string { return strings.SplitN(ansi.Strip(s), "\n", 2)[0] }
			dock := f.approval
			if m.activeToolUI != nil {
				dock = m.activeToolUI.component.View()
			}
			if !f.dock.Empty() && view[f.dock.Min.Y] != firstRow(dock) {
				t.Fatalf("view row %d = %q, want the dock's first row", f.dock.Min.Y, view[f.dock.Min.Y])
			}
			if !f.editor.Empty() && view[f.editor.Min.Y] != firstRow(m.editor.View()) {
				t.Fatalf("view row %d = %q, want the editor's first row", f.editor.Min.Y, view[f.editor.Min.Y])
			}
		})
	}
}

func TestChatFooterRendersBelowEditor(t *testing.T) {
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
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})

	view := m.chatView()
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

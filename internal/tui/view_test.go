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

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// A frame's surfaces tile the screen top to bottom, View draws each one on the
// rows the frame gives it, and a frame that cannot be usably drawn is marked
// too small instead.
func TestLayoutTilesTheScreen(t *testing.T) {
	approval := func(m *Model) { m.syncApprovals([]agent.Block{animToolBlock(agent.ToolAwaitingApproval)}) }
	draft := func(m *Model) { setConversationInput(m, strings.Repeat("draft\n", 12)) }
	toolUI := func(m *Model) { m.openToolUI(questionRequest(t, "question")) }
	for _, tc := range []struct {
		name          string
		width, height int
		setup         []func(*Model)
		tooSmall      bool
	}{
		{"composer", 80, 24, nil, false},
		{"tall draft under the header", 80, 22, []func(*Model){draft}, false},
		{"approval", 80, 11, []func(*Model){approval}, false},
		{"tool UI", 80, 24, []func(*Model){toolUI}, false},
		{"short tool UI", 80, 14, []func(*Model){toolUI}, false},
		{"narrow chat", minimumChatWidth - 1, 24, nil, true},
		{"short approval", 80, 10, []func(*Model){approval}, true},
		{"narrow approval", approvalMinWidth - 1, 24, []func(*Model){approval}, true},
		{"approval squeezed by a draft", 80, 17, []func(*Model){approval, draft}, true},
		{"tool UI hides a tall draft", 80, 20, []func(*Model){toolUI, draft}, false},
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

			rects := slices.DeleteFunc([]image.Rectangle{f.transcript, f.prompt, f.composerGap, f.composer, f.footer}, image.Rectangle.Empty)
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
			if m.prompt() != nil && (f.prompt.Empty() || view[f.prompt.Min.Y] != firstRow(f.promptView)) {
				t.Fatalf("prompt %v: view row = %q, want the prompt's first row", f.prompt, view[f.prompt.Min.Y])
			}
			if f.replaced {
				// The prompt takes the composer's place, under the gap, over a blank footer.
				if !f.composer.Empty() || f.prompt.Min.Y != f.composerGap.Max.Y || strings.TrimSpace(view[f.footer.Min.Y]) != "" {
					t.Fatalf("prompt %v does not replace the composer: composer=%v gap=%v footer=%q", f.prompt, f.composer, f.composerGap, view[f.footer.Min.Y])
				}
			} else if view[f.composer.Min.Y] != firstRow(m.editor.View()) {
				t.Fatalf("view row %d = %q, want the editor's first row", f.composer.Min.Y, view[f.composer.Min.Y])
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

func TestFrameAtClassifiesEveryRow(t *testing.T) {
	f := frame{
		transcript:  image.Rect(0, 0, 10, 5),
		prompt:      image.Rect(0, 5, 10, 8),
		composerGap: image.Rect(0, 8, 10, 9),
		composer:    image.Rect(0, 9, 10, 11),
		footer:      image.Rect(0, 11, 10, 12),
	}
	for y, want := range map[int]region{
		0: regionTranscript, 4: regionTranscript,
		5: regionPrompt, 7: regionPrompt,
		8: regionChrome, 11: regionChrome,
		9: regionComposer, 10: regionComposer,
	} {
		if got := f.at(image.Pt(3, y)); got != want {
			t.Errorf("row %d = %v, want %v", y, got, want)
		}
	}
}

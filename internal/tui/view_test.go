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

	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

// A frame's surfaces tile the screen top to bottom, View draws each one on the
// rows the frame gives it, and a frame that cannot be usably drawn is marked
// too small instead.
func TestLayoutTilesTheScreen(t *testing.T) {
	approval := func(m *Model) { m.syncApprovals([]agent.Block{animToolBlock(agent.ToolAwaitingApproval)}) }
	draft := func(m *Model) { setConversationInput(m, strings.Repeat("draft\n", 12)) }
	queue := func(m *Model) {
		for i := range 8 {
			m.state.Queued = append(m.state.Queued, agent.QueuedPrompt{ID: agent.QueueID(i + 1), Message: "queued follow-up"})
		}
		m.syncQueue()
	}
	toolUI := func(m *Model) { m.openToolUI(questionRequest(t, "question")) }
	tasks := func(n int) func(*Model) {
		return func(m *Model) {
			for range n {
				m.state.Tasks = append(m.state.Tasks, agent.Task{Name: "Investigate errors", Status: agent.TaskRunning})
			}
		}
	}
	for _, tc := range []struct {
		name          string
		width, height int
		setup         []func(*Model)
		tooSmall      bool
	}{
		{"composer", 80, 24, nil, false},
		{"queued prompts", 80, 24, []func(*Model){queue}, false},
		{"queued prompts on short screen", 40, 8, []func(*Model){queue}, false},
		{"queue with approval", 80, 24, []func(*Model){queue, approval}, false},
		{"queue with replacing tool UI", 80, 30, []func(*Model){queue, toolUI}, false},
		{"tall draft under the header", 80, 22, []func(*Model){draft}, false},
		{"approval", 80, 11, []func(*Model){approval}, false},
		{"tool UI", 80, 24, []func(*Model){toolUI}, false},
		{"short tool UI", 80, 14, []func(*Model){toolUI}, false},
		{"narrow chat", minimumChatWidth - 1, 24, nil, true},
		{"short approval", 80, 10, []func(*Model){approval}, true},
		{"narrow approval", approvalMinWidth - 1, 24, []func(*Model){approval}, true},
		{"approval beside a tall draft", 80, 17, []func(*Model){approval, draft}, false},
		{"tool UI hides a tall draft", 80, 20, []func(*Model){toolUI, draft}, false},
		{"small tool UI", 30, 10, []func(*Model){toolUI}, true},
		{"running task", 80, 24, []func(*Model){tasks(1)}, false},
		{"tasks past the cap", 80, 24, []func(*Model){tasks(5)}, false},
		{"tasks on a short screen", 40, 12, []func(*Model){tasks(5)}, false},
		{"task under an approval", 80, 12, []func(*Model){tasks(1), approval}, false},
		{"tasks under an approval on a short screen", 80, 14, []func(*Model){tasks(5), approval}, false},
		{"task under a draft that fills the screen", 80, 13, []func(*Model){tasks(1), draft}, false},
		{"task under a replacing tool UI", 80, 24, []func(*Model){tasks(2), toolUI}, false},
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

			rects := slices.DeleteFunc([]image.Rectangle{f.transcript, f.queue, f.prompt, f.composerGap, f.composer, f.tray, f.footer}, image.Rectangle.Empty)
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
			if want := len(m.state.Tasks); want > 0 {
				// Full, unless full would leave the transcript too short; none
				// when only the transcript's own row is left.
				rows, room := min(want, trayLineCap+1), f.transcript.Dy()+f.tray.Dy()
				switch {
				case room <= 1:
					rows = 0
				case room-rows < trayTranscriptRows:
					rows = 1
				}
				if f.tray.Dy() != rows || (rows > 0 && (f.tray.Max.Y != f.footer.Min.Y || view[f.tray.Min.Y] != firstRow(f.trayView))) {
					t.Fatalf("tray %v over footer %v: %d rows, want %d; first row %q", f.tray, f.footer, f.tray.Dy(), rows, view[f.tray.Min.Y])
				}
			} else if !f.tray.Empty() {
				t.Fatalf("tray %v shows with nothing running", f.tray)
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
		agent.New(&spyBackend{t: t}, agent.Config{}),
		Config{Workspace: ws},
	)
	_ = m.editor.Focus()
	m.state.Usage = &assistant.Usage{TokensUsed: 330_000}
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
		text := chatFooterText("/tmp/private\x1b[31m\nworkspace", true, "", usage, width)
		if strings.ContainsAny(text, "\x1b\n\r") {
			t.Fatalf("width %d footer retained terminal controls: %q", width, text)
		}
		if got := ansi.StringWidth(text); got > width {
			t.Fatalf("width %d footer rendered %d cells: %q", width, got, text)
		}
	}
	for _, tc := range []struct {
		turbo bool
		width int
		want  string
	}{
		{width: 24, want: "…p/workspace · 330K used"},
		{turbo: true, width: 80, want: "/tmp/workspace · Turbo · 330K used"},
		{turbo: true, width: 30, want: "…workspace · Turbo · 330K used"},
	} {
		if got := chatFooterText("/tmp/workspace", tc.turbo, "", usage, tc.width); got != tc.want {
			t.Errorf("footer(turbo=%v, width=%d) = %q, want %q", tc.turbo, tc.width, got, tc.want)
		}
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
		follow:      image.Rect(2, 3, 8, 5),
		prompt:      image.Rect(0, 5, 10, 8),
		popup:       image.Rect(5, 6, 10, 9),
		composerGap: image.Rect(0, 8, 10, 9),
		composer:    image.Rect(0, 9, 10, 11),
		footer:      image.Rect(0, 11, 10, 12),
	}
	for _, tt := range []struct {
		at     image.Point
		region region
		local  image.Point
	}{
		{image.Pt(3, 0), regionTranscript, image.Pt(3, 0)},
		{image.Pt(1, 4), regionTranscript, image.Pt(1, 4)},
		{image.Pt(3, 4), regionFollow, image.Pt(1, 1)},
		{image.Pt(3, 5), regionPrompt, image.Pt(3, 0)},
		{image.Pt(3, 7), regionPrompt, image.Pt(3, 2)},
		{image.Pt(6, 7), regionPopup, image.Pt(1, 1)}, // the popup covers the prompt
		{image.Pt(3, 8), regionChrome, image.Pt(3, 8)},
		{image.Pt(3, 9), regionComposer, image.Pt(3, 0)},
		{image.Pt(3, 11), regionChrome, image.Pt(3, 11)},
	} {
		if region, local := f.at(tt.at); region != tt.region || local != tt.local {
			t.Errorf("%v = %v at %v, want %v at %v", tt.at, region, local, tt.region, tt.local)
		}
	}
}

// TestChatFooterUsesSecondaryText pins the row below the composer to the
// readable text level rather than the texture level.
func TestChatFooterUsesSecondaryText(t *testing.T) {
	for _, test := range []struct {
		name   string
		isDark bool
	}{
		{name: "dark", isDark: true},
		{name: "light", isDark: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			theme := styles.Default(test.isDark)
			m := newShell()
			m.applyStyles(theme)
			m.mode = ModeChat
			m.resize(80, 24)
			m.workspaceDisplayPath = "/tmp/workspace"

			footer := m.chatFooter()
			if !strings.Contains(footer, sgrForeground(t, theme.Text.Secondary.GetForeground())) {
				t.Errorf("footer is not painted with secondary text: %q", footer)
			}
			if strings.Contains(footer, sgrForeground(t, theme.Text.Tertiary.GetForeground())) {
				t.Errorf("footer still painted with tertiary text: %q", footer)
			}
		})
	}
}

// A popup hides the transcript rows it covers whole, so no fragment of them,
// such as a notice's marker, shows beside it.
func TestPopupCoversItsRows(t *testing.T) {
	for name, open := range map[string]func(*Model){
		"completion menu": func(m *Model) { m.Update(tea.KeyPressMsg{Code: '/', Text: "/"}) },
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := newPermissionsModel(t, agent.ModeManual)
			defer m.Detach()
			for range 30 {
				m.postNotice(notice(chat.NoticeInfo, nil, "Earlier message"))
			}
			open(m)
			m.relayout()
			popupRows := 0
			for _, row := range strings.Split(ansi.Strip(m.View().Content), "\n") {
				first, last := strings.IndexAny(row, "╭│╰"), strings.LastIndexAny(row, "╮│╯")
				if first < 0 {
					continue
				}
				popupRows++
				if strings.TrimSpace(row[:first]) != "" || strings.TrimSpace(row[last+len("│"):]) != "" {
					t.Fatalf("transcript shows beside the popup: %q", row)
				}
			}
			if popupRows == 0 {
				t.Fatal("setup: no popup drawn")
			}
		})
	}
}

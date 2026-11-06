package tui

import (
	"context"
	"image"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
)

func newExitCardModel(t *testing.T, width, height int) *Model {
	t.Helper()
	m := newModelWithSpy(t)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func drawnView(t *testing.T, m *Model) *uv.ScreenBuffer {
	t.Helper()
	buf := uv.NewScreenBuffer(m.width, m.height)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(m.View().Content).Draw(&buf, buf.Bounds())
	return &buf
}

// drawnCardRect finds the card in a stripped view from its title, two cells
// inside the border's left edge (border and padding), and from the border's
// corners.
func drawnCardRect(t *testing.T, view string) image.Rectangle {
	t.Helper()
	lines := strings.Split(view, "\n")
	for y, line := range lines {
		title := strings.Index(line, "Exit Bits?")
		if title < 0 {
			continue
		}
		x := len([]rune(line[:title])) - 2
		borderRow := []rune(lines[y-1])
		right := x + slices.Index(borderRow[x:], '╮') + 1
		for bottom := y; bottom < len(lines); bottom++ {
			if strings.ContainsRune(lines[bottom], '╰') {
				return image.Rect(x, y-1, right, bottom+1)
			}
		}
	}
	t.Fatalf("no card in the view:\n%s", view)
	return image.Rectangle{}
}

func openedCardRect(t *testing.T, m *Model) image.Rectangle {
	t.Helper()
	m.openExitCard()
	defer m.closeExitCard()
	return drawnCardRect(t, ansi.Strip(m.View().Content))
}

func TestExitCardRendersCopy(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"Exit Bits?",
		"Your conversation is saved. You can pick up where you left off anytime.",
		"Quit",
		"Keep working",
		"ESC x",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view does not contain %q:\n%s", want, view)
		}
	}
}

func TestExitCardHighlightFollowsSelection(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	m.exitCard.selected = exitKeepWorking
	buf := drawnView(t, m)
	fill := hexOf(m.styles.ExitCard.Selected.GetBackground())
	quit := buf.Lines[m.exitCard.quit.Min.Y][m.exitCard.quit.Min.X]
	keep := buf.Lines[m.exitCard.keep.Min.Y][m.exitCard.keep.Min.X]
	if hexOf(keep.Style.Bg) != fill || hexOf(quit.Style.Bg) == fill {
		t.Fatalf("the highlight fill did not move to Keep working: quit bg %s, keep bg %s, fill %s",
			hexOf(quit.Style.Bg), hexOf(keep.Style.Bg), fill)
	}
}

func TestExitCardCentered(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	rect := drawnCardRect(t, ansi.Strip(m.View().Content))
	if want := image.Pt((120-rect.Dx())/2, (40-rect.Dy())/2); rect.Min != want {
		t.Fatalf("card at %v, want %v", rect.Min, want)
	}
}

func TestExitCardHitRects(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	cells := func(r image.Rectangle) string {
		return string([]rune(lines[r.Min.Y])[r.Min.X:r.Max.X])
	}
	for name, tc := range map[string]struct {
		rect image.Rectangle
		want string
	}{
		"quit":    {m.exitCard.quit, " Quit "},
		"keep":    {m.exitCard.keep, " Keep working "},
		"dismiss": {m.exitCard.dismiss, "ESC x"},
	} {
		if got := cells(tc.rect); got != tc.want {
			t.Errorf("%s rect %v covers %q, want %q", name, tc.rect, got, tc.want)
		}
	}
}

func TestExitCardNarrowWraps(t *testing.T) {
	m := newExitCardModel(t, 44, 20)
	m.openExitCard()
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for _, line := range lines {
		if width := ansi.StringWidth(line); width > 44 {
			t.Fatalf("line is %d cells wide, want at most 44: %q", width, line)
		}
	}
	var words []string
	for _, line := range lines {
		words = append(words, strings.Fields(strings.Trim(line, " │"))...)
	}
	joined := strings.Join(words, " ")
	if !strings.Contains(joined, "Your conversation is saved. You can pick up where you left off anytime.") {
		t.Fatalf("the body was truncated, not wrapped:\n%s", strings.Join(lines, "\n"))
	}
}

func TestExitCardCompactKeepsKeyHint(t *testing.T) {
	m := newExitCardModel(t, 24, 20)
	m.openExitCard()
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(strings.Join(strings.Fields(view), " "), "enter quit · esc keep working") {
		t.Fatalf("the compact card lost its key hint:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 24 {
			t.Fatalf("line is %d cells wide, want at most 24: %q", width, line)
		}
	}
}

func TestExitCardCompactHintFollowsSelection(t *testing.T) {
	m := newExitCardModel(t, 24, 20)
	m.openExitCard()
	m.Update(exitCardKey("", tea.KeyTab))
	view := strings.Join(strings.Fields(ansi.Strip(m.View().Content)), " ")
	if !strings.Contains(view, "enter/esc keep working") || strings.Contains(view, "enter quit") {
		t.Fatalf("the compact hint did not follow the highlight to Keep working:\n%s", view)
	}
}

// styledText returns the cells of the closed view that hold text outside the
// card, where the fade is the only thing that can change them.
func styledText(t *testing.T, m *Model, card image.Rectangle) map[image.Point]uv.Cell {
	t.Helper()
	cells := map[image.Point]uv.Cell{}
	buf := drawnView(t, m)
	for y, line := range buf.Lines {
		for x, cell := range line {
			at := image.Pt(x, y)
			if at.In(card) || strings.TrimSpace(cell.Content) == "" || cell.Style.Fg == nil {
				continue
			}
			cells[at] = cell
		}
	}
	return cells
}

func TestExitCardFadesBehind(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	card := openedCardRect(t, m)
	before := styledText(t, m, card)
	if len(before) == 0 {
		t.Fatal("the closed view has no styled text to compare")
	}

	m.openExitCard()
	buf := drawnView(t, m)
	faded := 0
	for at, was := range before {
		now := buf.Lines[at.Y][at.X]
		if now.Content != was.Content {
			t.Fatalf("cell %v changed text from %q to %q", at, was.Content, now.Content)
		}
		if got, want := hexOf(now.Style.Fg), hexOf(fadeColor(was.Style.Fg, m.styles.Background)); got != want {
			t.Fatalf("cell %v fg = %s, want %s (faded from %s)", at, got, want, hexOf(was.Style.Fg))
		}
		if hexOf(now.Style.Fg) != hexOf(was.Style.Fg) {
			faded++
		}
	}
	if faded == 0 {
		t.Fatal("no cell behind the card changed color")
	}
}

func TestExitCardMonochromeSkipsFade(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.monochrome = true
	card := openedCardRect(t, m)
	before := styledText(t, m, card)
	if len(before) == 0 {
		t.Fatal("the closed view has no styled text to compare")
	}

	m.openExitCard()
	buf := drawnView(t, m)
	for at, was := range before {
		if got := hexOf(buf.Lines[at.Y][at.X].Style.Fg); got != hexOf(was.Style.Fg) {
			t.Fatalf("cell %v fg = %s, want it unfaded %s", at, got, hexOf(was.Style.Fg))
		}
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Exit Bits?") {
		t.Fatalf("the card is missing in monochrome:\n%s", view)
	}
}

func TestExitCardBlursEditor(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if m.focus() != focusExitCard {
		t.Fatalf("focus = %v, want focusExitCard", m.focus())
	}
	if m.editor.Focused() {
		t.Fatal("the editor kept focus behind the card")
	}
}

func TestExitCardSurvivesTooSmall(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	m.Update(tea.WindowSizeMsg{Width: 6, Height: 4})
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Resize") {
		t.Fatalf("too-small frame did not show the resize hint:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !m.exitCard.open {
		t.Fatal("the card closed across a resize")
	}
	rect := drawnCardRect(t, ansi.Strip(m.View().Content))
	if want := image.Pt((120-rect.Dx())/2, (40-rect.Dy())/2); rect.Min != want {
		t.Fatalf("card at %v, want it re-centered at %v", rect.Min, want)
	}
}

func exitCardKey(text string, code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Text: text, Code: code}
}

func quitsIn(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		return slices.ContainsFunc(msg, quitsIn)
	}
	return false
}

func TestExitCardEnterQuits(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	_, cmd := m.Update(exitCardKey("", tea.KeyEnter))
	if !quitsIn(cmd) {
		t.Fatal("enter on Quit did not quit")
	}
}

func TestExitCardEscCloses(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.editor.Update(tea.PasteMsg{Content: "half a prompt"})
	m.openExitCard()
	_, cmd := m.Update(exitCardKey("", tea.KeyEscape))
	if m.exitCard.open || quitsIn(cmd) {
		t.Fatalf("esc: open %v, quit %v", m.exitCard.open, quitsIn(cmd))
	}
	if got := m.editor.Value(); got != "half a prompt" {
		t.Fatalf("draft = %q, want it untouched", got)
	}
}

func TestExitCardTabThenEnterKeepsWorking(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	m.Update(exitCardKey("", tea.KeyTab))
	if m.exitCard.selected != exitKeepWorking {
		t.Fatalf("selected = %v after tab, want Keep working", m.exitCard.selected)
	}
	_, cmd := m.Update(exitCardKey("", tea.KeyEnter))
	if m.exitCard.open || quitsIn(cmd) {
		t.Fatalf("enter on Keep working: open %v, quit %v", m.exitCard.open, quitsIn(cmd))
	}
}

func TestExitCardArrowsAndShiftTabMoveHighlight(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"left", exitCardKey("", tea.KeyLeft)},
		{"right", exitCardKey("", tea.KeyRight)},
		{"shift+tab", tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newExitCardModel(t, 120, 40)
			m.openExitCard()
			m.Update(tc.key)
			if m.exitCard.selected != exitKeepWorking {
				t.Fatalf("selected = %v, want Keep working", m.exitCard.selected)
			}
			m.Update(tc.key)
			if m.exitCard.selected != exitQuit {
				t.Fatalf("selected = %v, want Quit after a second press", m.exitCard.selected)
			}
		})
	}
}

func TestExitCardTypingClosesAndTypes(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.editor.Update(tea.PasteMsg{Content: "fix "})
	m.openExitCard()
	m.Update(exitCardKey("t", 't'))
	if m.exitCard.open {
		t.Fatal("typing left the card open")
	}
	if got := m.editor.Value(); got != "fix t" {
		t.Fatalf("editor = %q, want %q", got, "fix t")
	}
}

func TestExitCardSpaceTypesButTabToggles(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.editor.Update(tea.PasteMsg{Content: "a"})
	m.openExitCard()
	m.Update(exitCardKey(" ", tea.KeySpace))
	if m.exitCard.open || m.editor.Value() != "a " {
		t.Fatalf("space: open %v, editor %q", m.exitCard.open, m.editor.Value())
	}

	m.openExitCard()
	m.Update(exitCardKey("", tea.KeyTab))
	if !m.exitCard.open || m.exitCard.selected != exitKeepWorking || m.editor.Value() != "a " {
		t.Fatalf("tab: open %v, selected %v, editor %q", m.exitCard.open, m.exitCard.selected, m.editor.Value())
	}
}

func TestExitCardPasteLandsInComposer(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.editor.Update(tea.PasteMsg{Content: "a"})
	m.openExitCard()
	m.Update(tea.PasteMsg{Content: "bc"})
	if m.exitCard.open {
		t.Fatal("paste left the card open")
	}
	if got := m.editor.Value(); got != "abc" {
		t.Fatalf("editor = %q, want %q", got, "abc")
	}
}

func TestExitCardDropsReflexInputForAPromptBehindIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{"navigation key", exitCardKey("j", 'j')},
		{"option key", exitCardKey("2", '2')},
		{"bracket", exitCardKey("]", ']')},
		{"paste", tea.PasteMsg{Content: "EU"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := startQuestions(t, agent.ModeSkipPermissions, questionInput, 1)
			waitQuestions(t, m)
			prompt := ansi.Strip(m.frame.promptView)
			if !strings.Contains(prompt, "› 1. US") {
				t.Fatalf("fixture does not start on the first option:\n%s", prompt)
			}
			editor := m.editor.Value()

			m.openExitCard()
			m.Update(tc.msg)

			if m.exitCard.open {
				t.Fatal("the card stayed open")
			}
			if dockedToolUI(m) == nil || m.focus() != focusPrompt {
				t.Fatalf("the prompt is not showing: focus %v", m.focus())
			}
			if got := ansi.Strip(m.frame.promptView); got != prompt {
				t.Fatalf("the prompt changed:\n%s\nwant\n%s", got, prompt)
			}
			if m.editor.Value() != editor {
				t.Fatalf("editor = %q, want %q", m.editor.Value(), editor)
			}
		})
	}
}

func TestExitCardClosesWhenLeavingChat(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	m.setMode(ModePermissions)
	if m.exitCard.open {
		t.Fatal("the card survived leaving chat")
	}
}

func clickAt(p image.Point) tea.MouseClickMsg {
	return tea.MouseClickMsg(tea.Mouse{X: p.X, Y: p.Y, Button: tea.MouseLeft})
}

func TestExitCardClicks(t *testing.T) {
	open := func() *Model {
		m := scrolledFollowModel()
		if m.follow.area.Empty() {
			t.Fatal("fixture shows no jump-to-latest control")
		}
		m.openExitCard()
		m.View()
		if m.exitCard.quit.Empty() || m.follow.area.Overlaps(image.Rect(0, m.exitCard.dismiss.Min.Y, m.width, m.exitCard.quit.Max.Y)) {
			t.Fatalf("fixture card %+v overlaps the control %v", m.exitCard, m.follow.area)
		}
		return m
	}

	m := open()
	_, cmd := m.Update(clickAt(m.exitCard.quit.Min))
	if !quitsIn(cmd) {
		t.Fatal("click on Quit did not quit")
	}

	for name, pick := range map[string]func(*Model) image.Rectangle{
		"keep":    func(m *Model) image.Rectangle { return m.exitCard.keep },
		"dismiss": func(m *Model) image.Rectangle { return m.exitCard.dismiss },
	} {
		m := open()
		_, cmd := m.Update(clickAt(pick(m).Min))
		if m.exitCard.open || quitsIn(cmd) {
			t.Fatalf("click on %s: open %v, quit %v", name, m.exitCard.open, quitsIn(cmd))
		}
	}

	m = open()
	top := m.list.VisibleSurface().Top
	for name, msg := range map[string]tea.Msg{
		"outside the card": clickAt(image.Pt(0, 0)),
		"jump-to-latest":   clickAt(m.follow.area.Min.Add(image.Pt(1, 1))),
		"wheel":            tea.MouseWheelMsg(tea.Mouse{X: 5, Y: 5, Button: tea.MouseWheelDown}),
		"motion":           tea.MouseMotionMsg(tea.Mouse{X: 5, Y: 5}),
	} {
		_, cmd := m.Update(msg)
		if !m.exitCard.open || quitsIn(cmd) {
			t.Fatalf("%s: open %v, quit %v", name, m.exitCard.open, quitsIn(cmd))
		}
		if m.list.VisibleSurface().Top != top || m.list.Following() {
			t.Fatalf("%s moved the transcript behind the card", name)
		}
		if click, ok := msg.(tea.MouseClickMsg); ok {
			m.Update(tea.MouseReleaseMsg(click))
		}
	}
}

func TestExitCardIgnoresSecondaryClicks(t *testing.T) {
	for _, button := range []tea.MouseButton{tea.MouseRight, tea.MouseMiddle} {
		m := newExitCardModel(t, 120, 40)
		m.openExitCard()
		m.View()
		at := m.exitCard.quit.Min
		_, cmd := m.Update(tea.MouseClickMsg(tea.Mouse{X: at.X, Y: at.Y, Button: button}))
		if !m.exitCard.open || quitsIn(cmd) {
			t.Fatalf("button %v on Quit: open %v, quit %v", button, m.exitCard.open, quitsIn(cmd))
		}
	}
}

func TestExitCardCtrlCStillQuits(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !quitsIn(cmd) {
		t.Fatal("ctrl+c with the card open did not quit")
	}
}

func TestExitCardOpenClearsSelection(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.selection.begin(testSelectionFrame("abc", 3, 1), 0, 0)
	if !m.selection.active() {
		t.Fatal("fixture selection is not active")
	}
	m.openExitCard()
	if m.selection.active() {
		t.Fatal("opening the card kept the selection")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Exit Bits?") {
		t.Fatalf("the card is missing:\n%s", view)
	}
}

func TestExitCardHitRectsResetWhenNotDrawn(t *testing.T) {
	m := newExitCardModel(t, 120, 40)
	m.openExitCard()
	m.View()
	if m.exitCard.quit.Empty() {
		t.Fatal("fixture drew no hit rectangles")
	}
	m.Update(tea.WindowSizeMsg{Width: 6, Height: 4})
	m.View()
	if !m.exitCard.quit.Empty() || !m.exitCard.keep.Empty() || !m.exitCard.dismiss.Empty() {
		t.Fatalf("stale hit rectangles survived an undrawn card: %+v", m.exitCard)
	}
}

func pressEsc(m *Model) {
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
}

func startBlockedTurn(t *testing.T) *Model {
	t.Helper()
	backend := newCancellableConversationBackend()
	m := New(agent.New(backend, agent.Config{ConversationID: "old"}))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	setConversationInput(m, "active prompt")
	_, _ = m.submit()
	waitSignal(t, backend.sendStarted)
	t.Cleanup(func() { drainConversationRemote(t, m) })
	return m
}

func startApprovalTurn(t *testing.T) *Model {
	t.Helper()
	m, _ := newPermissionsModel(t, agent.ModeManual)
	setConversationInput(m, "Run the action")
	_, _ = m.submit()
	t.Cleanup(func() {
		m.cancelOperation()
		drainConversationRemote(t, m)
	})
	return m
}

// startFork leaves a fork running: its command never runs, so the operation
// stays open until the test cancels it.
func startFork(t *testing.T) *Model {
	t.Helper()
	b := &tuiForkBackend{resumeBackend: newResumeBackend()}
	e := agent.New(b, agent.Config{ConversationID: forkSourceID})
	t.Cleanup(e.Close)
	m := New(e)
	t.Cleanup(m.Detach)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if _, cmd := m.dispatchCommand("fork", ""); cmd == nil || m.op.kind != opFork {
		t.Fatalf("fork did not start: op %+v", m.op)
	}
	t.Cleanup(m.op.cancel)
	return m
}

func TestEscOpensExitCardOnlyAtEndOfChain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T) *Model
		wantOpen bool
		check    func(t *testing.T, m *Model)
	}{
		{
			name:     "idle, empty composer",
			setup:    func(t *testing.T) *Model { return newExitCardModel(t, 120, 40) },
			wantOpen: true,
		},
		{
			name: "idle, draft",
			setup: func(t *testing.T) *Model {
				m := newExitCardModel(t, 120, 40)
				setConversationInput(m, "half")
				return m
			},
			wantOpen: true,
			check: func(t *testing.T, m *Model) {
				if got := m.editor.Value(); got != "half" {
					t.Fatalf("draft = %q, want it untouched", got)
				}
			},
		},
		{
			name:  "turn running",
			setup: startBlockedTurn,
			check: func(t *testing.T, m *Model) {
				if m.op.stop != stopAll {
					t.Fatal("esc did not cancel the running turn")
				}
			},
		},
		{
			name: "wind-down after a cancel",
			setup: func(t *testing.T) *Model {
				m := startBlockedTurn(t)
				pressEsc(m)
				if m.op.run == 0 || m.op.stop != stopAll {
					t.Fatalf("fixture is not winding down: run %v, stop %v", m.op.run, m.op.stop)
				}
				return m
			},
			wantOpen: true,
		},
		{
			name:  "fork running",
			setup: startFork,
			check: func(t *testing.T, m *Model) {
				if m.op.kind != opFork || m.op.stop != stopAll {
					t.Fatalf("esc did not cancel the fork: op %+v", m.op)
				}
			},
		},
		{
			name: "fork winding down after a cancel",
			setup: func(t *testing.T) *Model {
				m := startFork(t)
				pressEsc(m)
				if m.op.kind != opFork || m.op.stop != stopAll {
					t.Fatalf("fixture is not winding down: op %+v", m.op)
				}
				return m
			},
			wantOpen: true,
		},
		{
			name: "logout in flight",
			setup: func(t *testing.T) *Model {
				m := newExitCardModel(t, 120, 40)
				m.logout = func(context.Context) (bool, error, error) { return true, nil, nil }
				m.startLogout()
				if m.op.kind != opLogout {
					t.Fatalf("fixture is not logging out: op %+v", m.op)
				}
				t.Cleanup(m.op.cancel)
				return m
			},
		},
		{
			name: "logout queued after a cancelled turn",
			setup: func(t *testing.T) *Model {
				m := startBlockedTurn(t)
				m.logout = func(context.Context) (bool, error, error) { return true, nil, nil }
				m.requestLogout()
				if m.op.then != thenLogout || m.op.stop != stopAll {
					t.Fatalf("fixture has no logout queued: op %+v", m.op)
				}
				return m
			},
		},
		{
			name: "completion menu open",
			setup: func(t *testing.T) *Model {
				m := newExitCardModel(t, 120, 40)
				m.editor.Update(tea.PasteMsg{Content: "/q"})
				if !m.editor.MenuOpen() {
					t.Fatal("fixture menu is not open")
				}
				return m
			},
			check: func(t *testing.T, m *Model) {
				if m.editor.MenuOpen() {
					t.Fatal("esc left the completion menu open")
				}
			},
		},
		{
			name: "chips focused",
			setup: func(t *testing.T) *Model {
				m := newExitCardModel(t, 120, 40)
				m.editor.AddAttachment(editor.Attachment{Type: "service", ID: "a", Label: "a"})
				m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
				if !m.editor.ChipsFocused() {
					t.Fatal("fixture chips are not focused")
				}
				return m
			},
			check: func(t *testing.T, m *Model) {
				if m.editor.ChipsFocused() {
					t.Fatal("esc left the chips focused")
				}
			},
		},
		{
			name: "text selection active",
			setup: func(t *testing.T) *Model {
				m := newExitCardModel(t, 120, 40)
				m.selection.begin(testSelectionFrame("abc", 3, 1), 0, 0)
				if !m.selection.active() {
					t.Fatal("fixture selection is not active")
				}
				return m
			},
			check: func(t *testing.T, m *Model) {
				if m.selection.active() {
					t.Fatal("esc left the selection active")
				}
			},
		},
		{
			name: "approval prompt shown",
			setup: func(t *testing.T) *Model {
				m := startApprovalTurn(t)
				awaitPendingApproval(t, m)
				return m
			},
			check: func(t *testing.T, m *Model) {
				if waitingApprovals(m) != 0 {
					t.Fatal("the prompt did not receive the esc")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.setup(t)
			pressEsc(m)
			if m.exitCard.open != tc.wantOpen {
				t.Fatalf("card open = %v, want %v", m.exitCard.open, tc.wantOpen)
			}
			if tc.check != nil {
				tc.check(t, m)
			}
		})
	}
}

// scrolledFollowModel scrolls up from the tail and draws once, so the
// jump-to-latest control is on screen.
func scrolledFollowModel() *Model {
	m := followTestModel()
	m.resize(60, 30)
	m.relayout()
	m.list.ScrollToBottom()
	m.list.ScrollBy(-4)
	m.View()
	return m
}

func TestExitCardOpenClearsHover(t *testing.T) {
	t.Run("transcript row", func(t *testing.T) {
		m := accordionTestModel(t)
		m.Update(tea.MouseMotionMsg{X: 0, Y: accordionRow(t, m)})
		if !m.list.Hovered() || !m.pointerIsHand {
			t.Fatalf("fixture is not hovering a row: hovered %v, hand %v", m.list.Hovered(), m.pointerIsHand)
		}
		m.openExitCard()
		m.Update(tea.MouseMotionMsg{X: 0, Y: accordionRow(t, m)})
		if m.list.Hovered() || m.pointerIsHand {
			t.Fatalf("hover survived the card: hovered %v, hand %v", m.list.Hovered(), m.pointerIsHand)
		}
	})

	t.Run("follow control", func(t *testing.T) {
		m := scrolledFollowModel()
		m.Update(tea.MouseMotionMsg{X: m.follow.area.Min.X + 1, Y: m.follow.area.Min.Y + 1})
		if !m.follow.hover || !m.pointerIsHand {
			t.Fatalf("fixture is not hovering the control: hover %v, hand %v", m.follow.hover, m.pointerIsHand)
		}
		m.openExitCard()
		m.Update(tea.MouseMotionMsg{X: 0, Y: 0})
		if m.follow.hover || m.pointerIsHand {
			t.Fatalf("hover survived the card: hover %v, hand %v", m.follow.hover, m.pointerIsHand)
		}
	})
}

func TestExitCardOpenDropsStaleFollowPress(t *testing.T) {
	m := scrolledFollowModel()
	m.Update(clickAt(m.follow.area.Min.Add(image.Pt(1, 1))))
	if m.follow.pressed.Empty() {
		t.Fatal("fixture did not press the control")
	}
	m.openExitCard()
	if !m.follow.pressed.Empty() {
		t.Fatal("a press survived the card")
	}
}

func TestExitCardSuppressesHandWhileOpen(t *testing.T) {
	t.Run("transcript row", func(t *testing.T) {
		m := accordionTestModel(t)
		m.openExitCard()
		m.list.SetPointer(0, accordionRow(t, m))
		if !m.list.Hovered() {
			t.Fatal("fixture row is not hovered")
		}
		m.reconcilePointerShape()
		if m.pointerIsHand {
			t.Fatal("the hand showed over a hovered row while the card was open")
		}
	})

	t.Run("follow control", func(t *testing.T) {
		m := scrolledFollowModel()
		m.openExitCard()
		m.follow.hover = true
		m.reconcilePointerShape()
		if m.pointerIsHand {
			t.Fatal("the hand showed over the control while the card was open")
		}
	})
}

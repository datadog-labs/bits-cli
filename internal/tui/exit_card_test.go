package tui

import (
	"image"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
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

// drawnCardRect finds the card in a stripped view from its title, one cell
// inside the border's top-left corner on the next row down, and from the
// border's corners.
func drawnCardRect(t *testing.T, view string) image.Rectangle {
	t.Helper()
	lines := strings.Split(view, "\n")
	for y, line := range lines {
		runes := []rune(line)
		title := strings.Index(string(runes), "Exit Bits?")
		if title < 0 {
			continue
		}
		x := len([]rune(string(runes)[:title])) - 2
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

// openedCardRect opens the card, reports where it draws, and closes it again.
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
		"Quit Enter",
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
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Keep working Enter") || strings.Contains(view, "Quit Enter") {
		t.Fatalf("the Enter hint did not follow the highlight to Keep working:\n%s", view)
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
		"quit":    {m.exitCard.quit, " Quit Enter "},
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
		if got, want := hexOf(now.Style.Fg), hexOf(blendHalf(was.Style.Fg, m.styles.Background)); got != want {
			t.Fatalf("cell %v fg = %s, want %s (half-way from %s)", at, got, want, hexOf(was.Style.Fg))
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

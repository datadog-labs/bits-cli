package tui

import (
	"image"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func testSelectionFrame(content string, width, height int) selectionFrame {
	return newSelectionFrame(content, width, height, nil, 0)
}

func TestSelectionCopiesRowsInEitherDirection(t *testing.T) {
	frame := testSelectionFrame("abcde\nfghij\nklmno", 5, 3)

	tests := []struct {
		name   string
		anchor image.Point
		focus  image.Point
		want   string
	}{
		{name: "forward", anchor: image.Pt(3, 0), focus: image.Pt(1, 1), want: "de\nfg"},
		{name: "backward", anchor: image.Pt(1, 1), focus: image.Pt(3, 0), want: "de\nfg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got selection
			got.begin(frame, tt.anchor.X, tt.anchor.Y)
			if !got.selecting() {
				t.Fatal("begin did not start selection")
			}
			if text := got.finish(frame, tt.focus.X, tt.focus.Y, frame); text != tt.want {
				t.Fatalf("finish() = %q, want %q", text, tt.want)
			}
			if got.selecting() {
				t.Fatal("finish left mouse drag active")
			}
			if !got.selected() {
				t.Fatal("finish did not retain non-empty selection")
			}
		})
	}
}

func TestSelectionRemainsRenderedAfterFinishUntilCleared(t *testing.T) {
	frame := testSelectionFrame("hello", 5, 1)
	var got selection
	got.begin(frame, 1, 0)
	if text := got.finish(frame, 3, 0, frame); text != "ell" {
		t.Fatalf("finish() = %q, want %q", text, "ell")
	}
	if got.selecting() {
		t.Fatal("finished selection still reports active drag")
	}
	if !got.selected() {
		t.Fatal("finished selection was not retained")
	}
	if rendered := got.render(frame); !strings.Contains(rendered, "7") {
		t.Fatalf("render() = %q, want retained reverse highlight", rendered)
	}

	got.clear()
	if got.selected() || got.selecting() {
		t.Fatal("clear did not remove persistent selection")
	}
	if rendered := got.render(frame); rendered != frame.content {
		t.Fatalf("render() after clear = %q, want original content", rendered)
	}

	got.begin(frame, 4, 0)
	if got.selected() {
		t.Fatal("new click retained the previous selection")
	}
	if !got.selecting() {
		t.Fatal("new click did not start a drag")
	}
}

func TestSelectionUsesVirtualRowsAfterScrolling(t *testing.T) {
	visible := newSelectionFrame("FGHIJ\nKLMNO", 5, 2, []int{5, 6}, 0)
	document := testSelectionFrame("a0000\nb1111\nc2222\nd3333\ne4444\nfghij\nklmno\np7777", 5, 8)

	var got selection
	got.begin(visible, 1, 0)
	if rendered := got.render(visible); ansi.Strip(rendered) != "FGHIJ\nKLMNO" {
		t.Fatalf("single-point render changed visible frame: %q", rendered)
	}
	if text := got.finish(visible, 1, 1, document); text != "ghij\nkl" {
		t.Fatalf("finish() = %q, want %q", text, "ghij\nkl")
	}
}

func TestSelectionRendersReverseAndCopiesANSIText(t *testing.T) {
	frame := testSelectionFrame("\x1b[31mA界B\x1b[0m", 5, 1)
	var got selection
	got.begin(frame, 2, 0) // The continuation cell of 界.
	got.extend(frame, 3, 0)

	rendered := got.render(frame)
	if ansi.Strip(rendered) != "A界B " {
		t.Fatalf("render() stripped text = %q, want %q", ansi.Strip(rendered), "A界B ")
	}
	if !strings.Contains(rendered, "7") {
		t.Fatalf("render() = %q, want reverse attribute", rendered)
	}
	if text := got.finish(frame, 3, 0, frame); text != "界B" {
		t.Fatalf("finish() = %q, want %q", text, "界B")
	}
}

func TestSelectionUsesGraphemeWidths(t *testing.T) {
	family := "👨‍👩‍👧‍👦"
	frame := testSelectionFrame(family+"x", 3, 1)

	var got selection
	got.begin(frame, 0, 0)
	if text := got.finish(frame, 2, 0, frame); text != family+"x" {
		t.Fatalf("finish() = %q, want %q", text, family+"x")
	}
}

func TestSelectionClickWithoutMovementCopiesNothing(t *testing.T) {
	frame := testSelectionFrame("hello", 5, 1)
	var got selection
	start := time.Unix(100, 0)
	got.clicks.next(start, image.Pt(2, 0), selectionScopeTranscript)
	got.begin(frame, 2, 0)
	if text := got.finish(frame, 2, 0, frame); text != "" {
		t.Fatalf("finish() = %q, want empty text", text)
	}
	if got.selecting() {
		t.Fatal("click selection remained active")
	}
	if got.selected() {
		t.Fatal("click selection was retained")
	}
	if count := got.clicks.next(start.Add(100*time.Millisecond), image.Pt(2, 0), selectionScopeTranscript); count != 2 {
		t.Fatalf("click after a no-movement release = %d, want 2", count)
	}
}

func TestSelectionGestureStaysInOriginPane(t *testing.T) {
	frame := testSelectionFrame("aaaaa\nbbbbb\nccccc\nddddd", 5, 4)

	var transcript selection
	transcript.beginGesture(frame, selectionScopeTranscript, 0, 0, 2, 4)
	transcript.extendGesture(frame, 4, 3, 2, 4)
	if transcript.focus != image.Pt(4, 1) || transcript.edge != 1 {
		t.Fatalf("transcript focus/edge = %v/%d, want (4,1)/1", transcript.focus, transcript.edge)
	}
	if text := transcript.finishGesture(frame, 4, 3, 2, 4, frame); text != "aaaaa\nbbbbb" {
		t.Fatalf("transcript selection = %q", text)
	}

	var lower selection
	lower.beginGesture(frame, selectionScopeLower, 4, 3, 2, 4)
	lower.extendGesture(frame, 0, 0, 2, 4)
	if lower.focus != image.Pt(0, 2) || lower.edge != 0 {
		t.Fatalf("lower focus/edge = %v/%d, want (0,2)/0", lower.focus, lower.edge)
	}
	if text := lower.finishGesture(frame, 0, 0, 2, 4, frame); text != "ccccc\nddddd" {
		t.Fatalf("lower selection = %q", text)
	}
}

func TestSelectionAutoScrollReversesWithPointer(t *testing.T) {
	frame := testSelectionFrame("aaaaa\nbbbbb\nccccc", 5, 3)
	var got selection

	got.beginGesture(frame, selectionScopeTranscript, 0, 1, 3, 3)
	got.extendGesture(frame, 4, 2, 3, 3)
	if got.edge != 1 {
		t.Fatalf("forward edge = %d, want 1", got.edge)
	}

	got.extendGesture(frame, 0, 0, 3, 3)
	if got.edge != -1 {
		t.Fatalf("reversed edge = %d, want -1", got.edge)
	}
}

// The splash panel occupies the transcript document's first rows. Its cells are
// Kitty placeholder runes carrying an image id, so copying them yields garbage;
// a selection must start at the first transcript row instead.
func TestSelectionSkipsTheHeaderRows(t *testing.T) {
	const headerRows = 2
	frame := newSelectionFrame("PANEL\nPANEL\nfghij\nklmno", 5, 4, nil, headerRows)

	var got selection
	// Drag from inside the panel down through both transcript rows.
	got.begin(frame, 0, 0)
	text := got.finish(frame, 4, 3, frame)

	if strings.Contains(text, "PANEL") {
		t.Fatalf("selection copied header content: %q", text)
	}
	if text != "fghij\nklmno" {
		t.Fatalf("selection = %q, want the transcript rows only", text)
	}
}

// A selection wholly inside the header has nothing to copy.
func TestSelectionInsideTheHeaderCopiesNothing(t *testing.T) {
	frame := newSelectionFrame("PANEL\nPANEL\nfghij", 5, 3, nil, 2)

	var got selection
	got.begin(frame, 0, 0)
	if text := got.finish(frame, 4, 1, frame); text != "" {
		t.Fatalf("selection inside the header copied %q", text)
	}
}

// Header rows are excluded from the transcript's row mapping, so the panel is
// never painted as selected.
func TestHeaderRowsAreNotSelectableInTheVisibleFrame(t *testing.T) {
	m := welcomeModel(120, 40)
	m.mode = ModeChat
	m.resume = *resumeFixture(8)
	m.layoutTranscript()

	frame := m.visibleSelectionFrame(selectionScopeTranscript)
	headerRows := m.list.HeaderRows()
	if headerRows == 0 {
		t.Fatal("no header to exclude")
	}
	for y := range headerRows {
		if row := frame.virtualRow(y); row != -1 {
			t.Fatalf("screen row %d maps to document row %d, want -1 (header)", y, row)
		}
	}
	if row := frame.virtualRow(headerRows); row != headerRows {
		t.Fatalf("first transcript row maps to %d, want %d", row, headerRows)
	}
}

// Excluding the header from the copy is not enough: an anchor inside it must
// also leave the rows unpainted.
func TestSelectionStartedInTheHeaderDoesNotPaintIt(t *testing.T) {
	frame := newSelectionFrame("PANEL\nPANEL\nfghij\nklmno", 5, 4, []int{-1, -1, 2, 3}, 2)

	var got selection
	got.begin(frame, 0, 0)
	got.extend(frame, 4, 3)

	lines := strings.Split(got.render(frame), "\n")
	for i := range 2 {
		if strings.Contains(lines[i], "\x1b[7m") {
			t.Fatalf("header row %d painted as selected: %q", i, lines[i])
		}
	}
	if !strings.Contains(lines[2], "\x1b[7m") {
		t.Fatalf("first transcript row not painted: %q", lines[2])
	}
}

func TestClickTrackerRecognizesOnlyNearbySameRowClicks(t *testing.T) {
	start := time.Unix(100, 0)
	tests := []struct {
		name   string
		points []image.Point
		scopes []selectionScope
		waits  []time.Duration
		want   []int
	}{
		{
			name:   "double and triple",
			points: []image.Point{image.Pt(4, 2), image.Pt(6, 2), image.Pt(5, 2), image.Pt(5, 2)},
			scopes: []selectionScope{selectionScopeTranscript, selectionScopeTranscript, selectionScopeTranscript, selectionScopeTranscript},
			waits:  []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond},
			want:   []int{1, 2, 3, 1},
		},
		{
			name:   "new row",
			points: []image.Point{image.Pt(4, 2), image.Pt(4, 3)},
			scopes: []selectionScope{selectionScopeTranscript, selectionScopeTranscript},
			waits:  []time.Duration{0, 100 * time.Millisecond},
			want:   []int{1, 1},
		},
		{
			name:   "new pane",
			points: []image.Point{image.Pt(4, 2), image.Pt(4, 2)},
			scopes: []selectionScope{selectionScopeTranscript, selectionScopeLower},
			waits:  []time.Duration{0, 100 * time.Millisecond},
			want:   []int{1, 1},
		},
		{
			name:   "timeout",
			points: []image.Point{image.Pt(4, 2), image.Pt(4, 2)},
			scopes: []selectionScope{selectionScopeTranscript, selectionScopeTranscript},
			waits:  []time.Duration{0, multiClickInterval + time.Millisecond},
			want:   []int{1, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tracker clickTracker
			for i, point := range tt.points {
				got := tracker.next(start.Add(tt.waits[i]), point, tt.scopes[i])
				if got != tt.want[i] {
					t.Fatalf("click %d = %d, want %d", i+1, got, tt.want[i])
				}
			}
		})
	}
}

func TestWordRangeUsesRenderedCells(t *testing.T) {
	frame := testSelectionFrame("\x1b[31mhello\x1b[0m, 世界\nwrapped", 11, 2)
	tests := []struct {
		name string
		at   image.Point
		want string
	}{
		{name: "word", at: image.Pt(1, 0), want: "hello"},
		{name: "punctuation", at: image.Pt(5, 0), want: ","},
		{name: "wide grapheme continuation", at: image.Pt(8, 0), want: "世"},
		{name: "wrapped row", at: image.Pt(3, 1), want: "wrapped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			anchor, focus, ok := wordRange(frame, tt.at)
			if !ok {
				t.Fatal("wordRange() did not find a word")
			}
			if got := extractSelection(frame, anchor, focus); got != tt.want {
				t.Fatalf("selected text = %q, range %v-%v, want %q", got, anchor, focus, tt.want)
			}
		})
	}
	if _, _, ok := wordRange(frame, image.Pt(6, 0)); ok {
		t.Fatal("whitespace produced a word selection")
	}
	anchor, focus, _ := wordRange(frame, image.Pt(5, 0))
	var selected selection
	selected.beginRange(anchor, focus)
	if text := selected.finishGesture(frame, 5, 0, 2, 2, frame); text != "," {
		t.Fatalf("single-cell word selection copied %q, want comma", text)
	}
	if !selected.selected() {
		t.Fatal("single-cell word selection was not retained")
	}
}

func TestMultiClickRangeIsPreservedOnRelease(t *testing.T) {
	frame := testSelectionFrame("first line\nsecond line", 11, 2)
	var got selection
	got.scope = selectionScopeTranscript
	got.clickPoint = image.Pt(5, 1)
	got.beginRange(image.Pt(0, 1), image.Pt(10, 1))
	if text := got.finishGesture(frame, 5, 1, 2, 2, frame); text != "second line" {
		t.Fatalf("finishGesture() = %q, want %q", text, "second line")
	}
}

func TestMultiClickDisplacedReleaseEndsClickSequence(t *testing.T) {
	frame := testSelectionFrame("hello", 5, 1)
	start := time.Unix(100, 0)
	var got selection
	got.clicks.next(start, image.Pt(1, 0), selectionScopeTranscript)
	got.beginClick(frame, selectionScopeTranscript, 1, 0, 1, 1, start.Add(100*time.Millisecond))
	if !got.keepRange {
		t.Fatal("double-click did not select a range")
	}
	if text := got.finishGesture(frame, 4, 0, 1, 1, frame); text != "hello" {
		t.Fatalf("finishGesture() = %q, want %q", text, "hello")
	}
	if got.clicks.count != 0 {
		t.Fatalf("displaced release left click count at %d", got.clicks.count)
	}
}

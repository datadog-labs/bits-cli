package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

// doneActionList builds a list whose transcript ends in a single actionable
// done notice, anchored to the one response block. It returns the list and
// that block's ID, which DoneButtonAt and DoneButtonAnchor must report back
// as the button's anchor.
func doneActionList(t *testing.T, width int) (*List, agent.BlockID) {
	t.Helper()
	l := NewList()
	l.SetStyles(DefaultStyles(true))
	l.SetWidth(width)
	l.SetHeight(10)
	anchor := textBlock("response", "the answer")
	notice := NoticeItem{ID: 1, After: anchor.ID, Notice: Notice{Level: NoticeDone, Text: "Done · Oct 26, 3:12 PM", Action: true}}
	l.SetTranscript([]agent.Block{anchor}, nil, []NoticeItem{notice})
	return l, anchor.ID
}

// buttonCellsFromRender scans the list's real rendered viewport for the
// fork button's three rows — the rounded corners and the glyph row — rather
// than recomputing its span from doneActionButtonArea, so these tests catch
// a List-side hit-test that disagrees with what is actually on screen.
func buttonCellsFromRender(t *testing.T, rows []string) (top, glyphX int) {
	t.Helper()
	glyphRow := -1
	for y, row := range rows {
		if strings.Contains(row, "⑂") {
			glyphRow = y
			break
		}
	}
	if glyphRow <= 0 || glyphRow >= len(rows)-1 {
		t.Fatalf("setup: no fork glyph with a row above and below in:\n%s", strings.Join(rows, "\n"))
	}
	stripped := ansi.Strip(rows[glyphRow])
	byteIdx := strings.Index(stripped, "⑂")
	if byteIdx < 0 {
		t.Fatalf("setup: glyph row lost its glyph: %q", stripped)
	}
	// Column, not byte offset: "✦" and "·" ahead of the button are each one
	// cell wide but more than one byte in UTF-8.
	x := ansi.StringWidth(stripped[:byteIdx])
	if x < 2 {
		t.Fatalf("setup: glyph too close to the row start: %q", stripped)
	}
	if got := ansi.Strip(rows[glyphRow-1]); !strings.Contains(got, "╭") {
		t.Fatalf("setup: no top border above the glyph row: %q", got)
	}
	if got := ansi.Strip(rows[glyphRow+1]); !strings.Contains(got, "╰") {
		t.Fatalf("setup: no bottom border below the glyph row: %q", got)
	}
	return glyphRow - 1, x
}

// TestDoneButtonAtMatchesRenderedButton pins DoneButtonAt to the button's
// real on-screen footprint: every cell of all three rendered rows hits
// (border rows included, matching doneActionButtonArea's full rectangle),
// while a column just outside either edge, and the rows immediately above
// and below the button, all miss.
func TestDoneButtonAtMatchesRenderedButton(t *testing.T) {
	const width = 80
	l, anchor := doneActionList(t, width)
	rows := strings.Split(l.Render(), "\n")
	top, glyphX := buttonCellsFromRender(t, rows)
	// The glyph sits two cells into the box (border, pad, glyph); the width
	// comes from the shared constant so this cannot drift from the label.
	left := glyphX - 2
	right := left + doneButtonWidth

	for y := top; y < top+3; y++ {
		for x := left; x < right; x++ {
			got, ok := l.DoneButtonAt(x, y)
			if !ok || got != anchor {
				t.Fatalf("(%d,%d) = (%+v, %v), want (%+v, true)", x, y, got, ok, anchor)
			}
		}
	}

	for _, x := range []int{left - 1, right} {
		if _, ok := l.DoneButtonAt(x, top+1); ok {
			t.Fatalf("column %d (outside [%d,%d)) hit the button", x, left, right)
		}
	}
	for _, y := range []int{top - 1, top + 3} {
		if _, ok := l.DoneButtonAt(glyphX, y); ok {
			t.Fatalf("row %d (outside [%d,%d)) hit the button", y, top, top+3)
		}
	}
}

// TestHoveredDoneButtonFollowsPointer confirms HoveredDoneButton is exactly
// DoneButtonAt at the stationary pointer the list already tracks via
// SetPointer, mirroring HoveredLink.
func TestHoveredDoneButtonFollowsPointer(t *testing.T) {
	const width = 80
	l, anchor := doneActionList(t, width)
	rows := strings.Split(l.Render(), "\n")
	top, glyphX := buttonCellsFromRender(t, rows)

	if _, ok := l.HoveredDoneButton(); ok {
		t.Fatal("unset pointer (-1,-1) should not hover the button")
	}
	l.SetPointer(glyphX, top+1)
	got, ok := l.HoveredDoneButton()
	if !ok || got != anchor {
		t.Fatalf("HoveredDoneButton() = (%+v, %v), want (%+v, true)", got, ok, anchor)
	}
	l.SetPointer(glyphX-3, top+1)
	if _, ok := l.HoveredDoneButton(); ok {
		t.Fatal("pointer left of the button should not hover it")
	}
}

// TestDoneButtonAtIgnoresNonActionDoneNotice confirms a plain done line
// (Action unset, the common case — only the transcript's last one ever sets
// it) never offers a button to click, even over where one would sit.
func TestDoneButtonAtIgnoresNonActionDoneNotice(t *testing.T) {
	l := headerList(t, 80, 10)
	l.SetStyles(DefaultStyles(true))
	notice := NoticeItem{ID: 1, Notice: Notice{Level: NoticeDone, Text: "Done · Oct 26, 3:12 PM"}}
	l.SetTranscript(nil, nil, []NoticeItem{notice})
	for y := range 3 {
		for x := range 80 {
			if _, ok := l.DoneButtonAt(x, y); ok {
				t.Fatalf("(%d,%d) hit a button on a non-actionable done notice", x, y)
			}
		}
	}
	if _, ok := l.DoneButtonAnchor(); ok {
		t.Fatal("DoneButtonAnchor() found one with no actionable notice present")
	}
}

// TestDoneButtonActionFlipRepaints pins the critical bug where the line
// cache's identity omitted Notice.Action: flipping Action off on the same
// NoticeItem.ID must drop the three-row button form, not just repaint it
// inactive. doneButtonActive (Action && l.doneButtonActive) stays false
// both before and after the flip while nothing is hovered, so a cache key
// built only from doneButtonActive cannot see the change and keeps
// returning the stale three-row lines forever.
func TestDoneButtonActionFlipRepaints(t *testing.T) {
	const width = 80
	l := NewList()
	l.SetStyles(DefaultStyles(true))
	l.SetWidth(width)
	l.SetHeight(10)
	anchor := textBlock("response", "the answer")
	notice := NoticeItem{ID: 1, After: anchor.ID, Notice: Notice{Level: NoticeDone, Text: "Done · Oct 26, 3:12 PM", Action: true}}
	l.SetTranscript([]agent.Block{anchor}, nil, []NoticeItem{notice})
	if got := strings.Count(l.Render(), "⑂"); got != 1 {
		t.Fatalf("setup: got %d fork glyphs, want 1", got)
	}

	notice.Notice.Action = false
	l.SetTranscript([]agent.Block{anchor}, nil, []NoticeItem{notice})
	if got := strings.Count(l.Render(), "⑂"); got != 0 {
		t.Fatalf("Action=false still rendered %d fork glyph(s); stale cache entry kept the button form", got)
	}
}

// TestDoneButtonAnchorFindsTheActionableNotice confirms DoneButtonAnchor
// reports the actionable notice's anchor independent of pointer position,
// the accessor the model uses to drop a stuck press once the button it was
// pressed on stops existing.
func TestDoneButtonAnchorFindsTheActionableNotice(t *testing.T) {
	l, anchor := doneActionList(t, 80)
	got, ok := l.DoneButtonAnchor()
	if !ok || got != anchor {
		t.Fatalf("DoneButtonAnchor() = (%+v, %v), want (%+v, true)", got, ok, anchor)
	}
}

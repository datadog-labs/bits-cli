package catalog

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestStatusPillMotionPageIsLabelledADesignProposal(t *testing.T) {
	got := groupStatusPillMotion.title()
	if !strings.HasPrefix(got, "[Design proposal]") {
		t.Errorf("title = %q, want it to start with %q", got, "[Design proposal]")
	}
}

// TestStatusPillMotionPageMarksTheChosenOption is the page's whole purpose:
// a reader must be able to tell which candidate is being built from which were
// only considered.
func TestStatusPillMotionPageMarksTheChosenOption(t *testing.T) {
	body := ansi.Strip(renderGroup(groupStatusPillMotion, 100, styles.Default(true), 0))

	if got := strings.Count(body, selectedMarker); got != 1 {
		t.Errorf("body marks %d rows as selected, want exactly 1", got)
	}
	if got := strings.Count(body, optionMarker); got < 2 {
		t.Errorf("body marks %d rows as options, want at least 2", got)
	}
}

// TestStatusPillMotionPageAnimates keeps the page honest: a design proposal
// about motion that renders a still frame teaches nothing.
func TestStatusPillMotionPageAnimates(t *testing.T) {
	seen := map[string]bool{}
	for frame := range styles.Default(true).Chat.StatusRunningLabel.Len() {
		seen[renderGroup(groupStatusPillMotion, 100, styles.Default(true), frame)] = true
	}
	if len(seen) < 2 {
		t.Errorf("page rendered %d distinct bodies across a sweep, want at least 2", len(seen))
	}
}

// TestStatusPillMotionPageHeightIsFrameInvariant protects the catalog's
// line-based scroll model: if the body's height changed per frame, the scroll
// offset would drift while the page animated.
func TestStatusPillMotionPageHeightIsFrameInvariant(t *testing.T) {
	theme := styles.Default(true)
	want := lineCount(renderGroup(groupStatusPillMotion, 100, theme, 0))
	for _, frame := range []int{1, 13, 40, 59} {
		if got := lineCount(renderGroup(groupStatusPillMotion, 100, theme, frame)); got != want {
			t.Errorf("frame %d body height = %d, want %d", frame, got, want)
		}
	}
}

// TestStatusPillMotionRowsKeepTheirWidth is the same anti-reflow guarantee the
// production chip has, applied to the illustrative option rows too.
func TestStatusPillMotionRowsKeepTheirWidth(t *testing.T) {
	theme := styles.Default(true)
	widths := map[int][]int{}
	for _, frame := range []int{0, 5, 23, 47} {
		for i, line := range strings.Split(renderGroup(groupStatusPillMotion, 100, theme, frame), "\n") {
			widths[i] = append(widths[i], ansi.StringWidth(line))
		}
	}
	for i, seen := range widths {
		for _, w := range seen {
			if w != seen[0] {
				t.Errorf("line %d width varies across frames: %v", i, seen)
				break
			}
		}
	}
}

// TestOtherPagesIgnoreTheFrame confines the animation to the one page that
// needs it.
func TestOtherPagesIgnoreTheFrame(t *testing.T) {
	theme := styles.Default(true)
	for g := group(0); g < numGroups; g++ {
		if g.animates() {
			continue
		}
		want := renderGroup(g, 100, theme, 0)
		for _, frame := range []int{1, 20, 55} {
			if got := renderGroup(g, 100, theme, frame); got != want {
				t.Errorf("page %q changed at frame %d", g.title(), frame)
			}
		}
	}
}

// TestOnlyTheMotionPageAnimates pins which pages declare themselves animated,
// so adding a page does not accidentally start a repaint loop.
func TestOnlyTheMotionPageAnimates(t *testing.T) {
	for g := group(0); g < numGroups; g++ {
		if got, want := g.animates(), g == groupStatusPillMotion; got != want {
			t.Errorf("page %q animates() = %t, want %t", g.title(), got, want)
		}
	}
}

func TestCatalogArmsTickOnlyOnTheAnimatedPage(t *testing.T) {
	m := New()
	if cmd := m.syncAnimation(); cmd != nil {
		t.Error("the catalog armed a tick on a static page")
	}

	m.group = groupStatusPillMotion
	if cmd := m.syncAnimation(); cmd == nil {
		t.Fatal("the catalog did not arm a tick on the animated page")
	}
	if cmd := m.syncAnimation(); cmd != nil {
		t.Error("a second sync armed a parallel tick chain")
	}
}

func TestCatalogTickAdvancesFrameAndRearms(t *testing.T) {
	m := New()
	m.group = groupStatusPillMotion
	m.syncAnimation()

	_, cmd := m.Update(stepMsg{generation: m.animGeneration})
	if cmd == nil {
		t.Fatal("a live tick should re-arm the chain")
	}
	if m.frame != 1 {
		t.Errorf("frame = %d, want 1", m.frame)
	}
}

func TestStaleCatalogTickIsDropped(t *testing.T) {
	m := New()
	m.group = groupStatusPillMotion
	m.syncAnimation()

	_, cmd := m.Update(stepMsg{generation: m.animGeneration - 1})
	if cmd != nil {
		t.Error("a stale tick should not re-arm the chain")
	}
	if m.frame != 0 {
		t.Errorf("frame = %d, want 0", m.frame)
	}
}

// TestNavigatingAwayFromTheMotionPageStopsTheTick keeps the other four pages
// completely idle, which is why the tick is gated on the visible page at all.
func TestNavigatingAwayFromTheMotionPageStopsTheTick(t *testing.T) {
	m := New()
	m.group = groupStatusPillMotion
	m.syncAnimation()
	if !m.animArmed {
		t.Fatal("expected the tick to be armed")
	}

	m.key("tab")
	if m.animArmed {
		t.Error("navigating to a static page left the tick armed")
	}
	if m.frame != 0 {
		t.Errorf("frame = %d, want it reset to 0", m.frame)
	}
}

// TestNavigatingOntoTheMotionPageStartsTheTick is the reverse: reaching the
// page through normal navigation must start it, not just setting the field.
func TestNavigatingOntoTheMotionPageStartsTheTick(t *testing.T) {
	m := New()
	m.group = prevGroup(groupStatusPillMotion)
	if cmd := m.key("tab"); cmd == nil {
		t.Fatal("navigating onto the animated page should arm the tick")
	}
	if !m.animArmed {
		t.Error("the tick should be armed after navigating onto the page")
	}
}

// TestSweepEndsDiffer catches the illustrative option rows collapsing to a
// single flat color, which would make them look broken next to the animated
// selected row.
func TestSweepEndsDiffer(t *testing.T) {
	for _, isDark := range []bool{true, false} {
		sty := chat.StylesFor(styles.Default(isDark))
		dim, hot := sweepEnds(sty)
		dr, dg, db, _ := dim.RGBA()
		hr, hg, hb, _ := hot.RGBA()
		if dr == hr && dg == hg && db == hb {
			t.Errorf("isDark=%t: sweep ends are the same color; option rows would be flat", isDark)
		}
	}
}

// headerLinesOf returns the rendered header line of every row on the page.
// Each row draws exactly one line naming the sample tool.
func headerLinesOf(body string) []string {
	var out []string
	for _, line := range strings.Split(ansi.Strip(body), "\n") {
		if strings.Contains(line, "search_logs") {
			out = append(out, line)
		}
	}
	return out
}

// TestRowsLeadWithTheProductionGlyph keeps the leading glyph out of the
// comparison. It is the same spinner in production and in every candidate, so
// the only thing differing between rows is the label treatment. The "today" row
// is the deliberate exception: it records the static dot that shipped before.
func TestRowsLeadWithTheProductionGlyph(t *testing.T) {
	const frame = 4
	theme := styles.Default(true)
	sty := chat.StylesFor(theme)
	spinner := sty.StatusSpinner.Frame(frame)

	headers := headerLinesOf(renderGroup(groupStatusPillMotion, 100, theme, frame))
	if len(headers) < 2 {
		t.Fatalf("found %d header lines, want one per row", len(headers))
	}

	var dots int
	for _, h := range headers {
		glyph := string([]rune(h)[0])
		switch glyph {
		case spinner:
		case "•":
			dots++
		default:
			t.Errorf("header %q leads with %q, want the spinner %q or the legacy dot", h, glyph, spinner)
		}
	}
	if dots != 1 {
		t.Errorf("%d rows lead with the static dot, want exactly 1 (the \"today\" row)", dots)
	}
}

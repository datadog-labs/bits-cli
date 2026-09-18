package chat

import (
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

func headerList(t *testing.T, width, height int) *List {
	t.Helper()
	l := NewList()
	l.SetWidth(width)
	l.SetHeight(height)
	return l
}

func textBlock(key, body string) agent.Block {
	return agent.Block{
		ID:       agent.BlockID{Scope: agent.ScopeLocal, Key: key, Kind: assistant.KindText},
		Role:     assistant.RoleUser,
		Kind:     assistant.KindText,
		Complete: true,
		Markdown: &assistant.MarkdownPayload{Content: body},
	}
}

// The header has no backing block, so every reader of presentationItem.start
// must tolerate the sentinel. An empty transcript is the case that would panic.
func TestHeaderRendersWithNoBlocks(t *testing.T) {
	l := headerList(t, 40, 10)
	l.SetHeader("PANEL LINE ONE\nPANEL LINE TWO")
	if !strings.Contains(l.Render(), "PANEL LINE ONE") {
		t.Fatalf("header missing from render:\n%s", l.Render())
	}
	if got := strings.Count(l.Render(), "\n") + 1; got != 10 {
		t.Fatalf("render returned %d lines, want the viewport height 10", got)
	}
}

// The render cache is keyed by (id, rev, width); a changed header must change
// rev or the stale panel keeps painting.
func TestHeaderChangeInvalidatesTheRenderCache(t *testing.T) {
	l := headerList(t, 40, 10)
	l.SetHeader("FIRST")
	_ = l.Render()
	l.SetHeader("SECOND")
	out := l.Render()
	if !strings.Contains(out, "SECOND") || strings.Contains(out, "FIRST") {
		t.Fatalf("stale header after change:\n%s", out)
	}
}

// Clearing the header must remove its rows, not leave a hole.
func TestHeaderCanBeCleared(t *testing.T) {
	l := headerList(t, 40, 10)
	l.SetHeader("PANEL")
	l.SetHeader("")
	if strings.Contains(l.Render(), "PANEL") {
		t.Fatal("cleared header still rendered")
	}
}

// Text selection maps screen rows through Document coordinates. The header is
// part of the document, so it must shift block rows by exactly its own height
// plus the inter-item gap, consistently in both Document and the surface.
func TestHeaderShiftsDocumentCoordinatesConsistently(t *testing.T) {
	l := headerList(t, 40, 40)
	l.SetItems([]agent.Block{textBlock("a", "BLOCK BODY")})
	before := strings.Index(l.Document(), "BLOCK BODY")
	if before < 0 {
		t.Fatal("block missing from document")
	}
	beforeRow := strings.Count(l.Document()[:before], "\n")

	l.SetHeader("H1\nH2\nH3")
	after := strings.Index(l.Document(), "BLOCK BODY")
	afterRow := strings.Count(l.Document()[:after], "\n")

	if shift := afterRow - beforeRow; shift != 4 {
		t.Fatalf("header shifted the block by %d rows, want 3 header rows + 1 gap", shift)
	}
	if !strings.Contains(l.Render(), "H1") {
		t.Fatal("header absent from the rendered surface")
	}
}

// firstVisibleLine is the transcript row the viewport starts on, with the
// blank fill rows the surface pads with ignored.
func firstVisibleLine(l *List) string {
	for _, line := range strings.Split(l.Render(), "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// Adding or removing the header renumbers every presentation item. A scrolled
// viewport must keep describing the same content row across that shift, or it
// jumps by a whole block when showSplashPanel flips on a resize.
func TestHeaderAppearanceKeepsTheScrolledRow(t *testing.T) {
	l := headerList(t, 40, 3)
	l.SetItems([]agent.Block{
		textBlock("a", "AAA"), textBlock("b", "BBB"), textBlock("c", "CCC"),
		textBlock("d", "DDD"), textBlock("e", "EEE"), textBlock("f", "FFF"),
	})
	l.ScrollBy(4) // two items plus their gaps: the viewport starts on CCC
	if got := firstVisibleLine(l); got != "CCC" {
		t.Fatalf("setup: viewport starts on %q, want CCC", got)
	}

	l.SetHeader("H1\nH2")
	if got := firstVisibleLine(l); got != "CCC" {
		t.Fatalf("the appearing header moved the viewport to %q, want CCC", got)
	}
	l.SetHeader("")
	if got := firstVisibleLine(l); got != "CCC" {
		t.Fatalf("the removed header moved the viewport to %q, want CCC", got)
	}
}

// Text selection needs to tell decoration from transcript, so the header's
// document rows must be countable the same way Document lays them out.
func TestHeaderRowsCountsTheHeaderAndItsGap(t *testing.T) {
	l := headerList(t, 40, 20)
	if got := l.HeaderRows(); got != 0 {
		t.Fatalf("HeaderRows without a header = %d, want 0", got)
	}

	l.SetItems([]agent.Block{textBlock("a", "BLOCK BODY")})
	l.SetHeader("H1\nH2\nH3")

	rows := l.HeaderRows()
	document := strings.Split(l.Document(), "\n")
	if rows < 3 || rows > len(document) {
		t.Fatalf("HeaderRows = %d, want at least the 3 header rows", rows)
	}
	// Everything the header owns, and nothing the transcript owns.
	for i := range rows {
		if strings.Contains(document[i], "BLOCK BODY") {
			t.Fatalf("document row %d is transcript content, inside the header's %d rows", i, rows)
		}
	}
	if !strings.Contains(strings.Join(document[rows:], "\n"), "BLOCK BODY") {
		t.Fatalf("transcript content is not at or after row %d:\n%s", rows, l.Document())
	}
}

// A resize can make the header appear while the user is scrolled inside the
// first transcript item. That item moves to index 1, so the intra-item line
// must move with it rather than be discarded.
func TestHeaderAppearingKeepsTheRowInsideTheFirstItem(t *testing.T) {
	l := headerList(t, 40, 4)
	l.SetItems([]agent.Block{textBlock("a", "L1\nL2\nL3\nL4\nL5\nL6\nL7\nL8\nL9\nL10")})
	// ScrollToTop clears follow; while the list still follows the tail, every
	// render re-anchors to the bottom and the offset under test is never read.
	l.ScrollToTop()
	l.ScrollBy(2)
	if l.Following() {
		t.Fatal("setup: the list is still following the tail")
	}

	top := strings.SplitN(l.Render(), "\n", 2)[0]
	if !strings.Contains(top, "L3") {
		t.Fatalf("setup: viewport starts on %q, want the third line", top)
	}

	l.SetHeader("H1\nH2")
	if got := strings.SplitN(l.Render(), "\n", 2)[0]; !strings.Contains(got, "L3") {
		t.Fatalf("viewport moved to %q when the header appeared, want to stay on L3", got)
	}
}

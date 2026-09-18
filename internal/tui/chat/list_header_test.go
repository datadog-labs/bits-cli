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
// plus the inter-item gap -- consistently in both Document and the surface.
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

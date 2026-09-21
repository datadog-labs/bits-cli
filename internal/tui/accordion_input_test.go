package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

func accordionTestBlock() agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: "search_logs", Status: agent.ToolSuccess, Output: "ok: 4 results"},
	}
}

// accordionDocumentLines counts Document()'s lines as a black-box proxy for
// the block's rendered height — chat.List keeps itemHeight unexported, so
// tests outside package chat observe collapse/expand through the rendered
// document instead of the internal line count.
func accordionDocumentLines(m *Model) int {
	return len(strings.Split(m.list.Document(), "\n"))
}

func accordionTestModel(t *testing.T) *Model {
	t.Helper()
	clearMultiplexerEnv(t)
	m := newShell()
	m.mode = ModeChat
	m.blocks = []agent.Block{accordionTestBlock()}
	m.resize(80, 24)
	m.syncTranscript()
	m.editor.Focus()
	if full := accordionDocumentLines(m); full <= 1 {
		t.Fatalf("fixture tool did not produce a disclosable body, document lines = %d", full)
	}
	m.list.Render() // populate zones for the current viewport
	return m
}

// clickAccordionZone simulates a plain click (press and release at the same
// coordinates, no intervening motion) rather than a drag: the toggle only
// fires at release, once finishSelection sees no real selection range formed.
func clickAccordionZone(m *Model, x, y int) {
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
}

// accordionRow locates the disclosure glyph's row in the document. The fixture
// model's transcript may lead with the splash/resume header, so the accordion
// row is not reliably row 0.
func accordionRow(t *testing.T, m *Model) int {
	t.Helper()
	for i, line := range strings.Split(m.list.Document(), "\n") {
		if plain := ansi.Strip(line); strings.ContainsAny(plain, "▼▶") {
			return i
		}
	}
	t.Fatal("no accordion row found in document")
	return -1
}

func TestClickOnAccordionZoneTogglesWithoutStartingSelection(t *testing.T) {
	m := accordionTestModel(t)
	full := accordionDocumentLines(m)
	row := accordionRow(t, m)

	clickAccordionZone(m, 0, row)

	if m.selection.selected() {
		t.Fatal("clicking the accordion zone left a text selection")
	}
	if got := accordionDocumentLines(m); got >= full {
		t.Fatalf("click did not collapse the block: document lines = %d, want fewer than %d", got, full)
	}

	m.list.Render()
	clickAccordionZone(m, 0, row)
	if got := accordionDocumentLines(m); got != full {
		t.Fatalf("second click did not re-expand the block: document lines = %d, want %d", got, full)
	}
}

func TestClickOffAccordionZoneStillStartsSelection(t *testing.T) {
	m := accordionTestModel(t)
	row := accordionRow(t, m)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 60, Y: row})

	if !m.selection.selecting() {
		t.Fatal("click off the accordion zone did not start a selection")
	}
}

func TestMouseMotionHoverSpansWholeRowAndClearsOffIt(t *testing.T) {
	m := accordionTestModel(t)
	row := accordionRow(t, m)
	resting := m.list.Document()

	m.Update(tea.MouseMotionMsg{X: 0, Y: row})
	overChevron := m.list.Document()
	if overChevron == resting {
		t.Fatal("motion over the chevron did not visibly change the row")
	}

	m.Update(tea.MouseMotionMsg{X: 60, Y: row})
	if got := m.list.Document(); got != overChevron {
		t.Fatal("motion elsewhere on the same header row changed or cleared the hover fill")
	}

	m.Update(tea.MouseMotionMsg{X: 60, Y: row + 1})
	if cleared := m.list.Document(); cleared != resting {
		t.Fatal("motion off the header row did not clear hover")
	}
}

func TestCtrlOTogglesAllToolsWithExpandFirstAlternation(t *testing.T) {
	m := accordionTestModel(t)
	full := accordionDocumentLines(m)

	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if got := accordionDocumentLines(m); got != full {
		t.Fatalf("first ctrl+o changed document lines to %d, want %d (expand-first is a no-op)", got, full)
	}

	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if got := accordionDocumentLines(m); got >= full {
		t.Fatalf("second ctrl+o document lines = %d, want fewer than %d", got, full)
	}
}

func TestViewSetsPointerShapeOverAnAccordionRow(t *testing.T) {
	m := accordionTestModel(t)
	row := accordionRow(t, m)

	if got := m.View().Content; !strings.Contains(got, ansi.SetPointerShape("default")) {
		t.Fatalf("resting view did not carry the default pointer shape: %q", got)
	}

	m.Update(tea.MouseMotionMsg{X: 0, Y: row})
	if got := m.View().Content; !strings.Contains(got, ansi.SetPointerShape("pointer")) {
		t.Fatalf("hovered view did not carry the pointer shape: %q", got)
	}

	m.Update(tea.MouseMotionMsg{X: 0, Y: row + 1})
	if got := m.View().Content; !strings.Contains(got, ansi.SetPointerShape("default")) {
		t.Fatalf("un-hovering the row did not reset the pointer shape: %q", got)
	}
}

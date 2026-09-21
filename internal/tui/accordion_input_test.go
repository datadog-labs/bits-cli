package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func TestClickOnAccordionZoneTogglesWithoutStartingSelection(t *testing.T) {
	m := accordionTestModel(t)
	full := accordionDocumentLines(m)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 0})

	if m.selection.selecting() {
		t.Fatal("clicking the accordion zone started a text selection")
	}
	if got := accordionDocumentLines(m); got >= full {
		t.Fatalf("click did not collapse the block: document lines = %d, want fewer than %d", got, full)
	}

	m.list.Render()
	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: 0})
	if got := accordionDocumentLines(m); got != full {
		t.Fatalf("second click did not re-expand the block: document lines = %d, want %d", got, full)
	}
}

func TestClickOffAccordionZoneStillStartsSelection(t *testing.T) {
	m := accordionTestModel(t)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 60, Y: 0})

	if !m.selection.selecting() {
		t.Fatal("click off the accordion zone did not start a selection")
	}
}

func TestMouseMotionSetsAndClearsHover(t *testing.T) {
	m := accordionTestModel(t)
	resting := m.list.Document()

	m.Update(tea.MouseMotionMsg{X: 0, Y: 0})
	if hovered := m.list.Document(); hovered == resting {
		t.Fatal("motion over the zone did not visibly change the gutter")
	}

	m.Update(tea.MouseMotionMsg{X: 60, Y: 0})
	if cleared := m.list.Document(); cleared != resting {
		t.Fatal("motion off the zone did not clear hover")
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

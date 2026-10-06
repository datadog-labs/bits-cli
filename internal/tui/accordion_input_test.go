package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// accordionTestOutput is long enough that the compact view hides rows, so the
// tool offers a disclosure control.
const accordionTestOutput = "row 1\nrow 2\nrow 3\nrow 4\nrow 5"

func accordionTestBlock() agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: "search_logs", Status: agent.ToolSuccess, Output: accordionTestOutput},
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
	m := newShell()
	m.mode = ModeChat
	m.transcript.Blocks = []agent.Block{accordionTestBlock()}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.syncTranscript()
	m.editor.Focus()
	if lines := accordionDocumentLines(m); lines <= 1 {
		t.Fatalf("fixture tool did not produce a compact body, document lines = %d", lines)
	}
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
	compact := accordionDocumentLines(m)
	row := accordionRow(t, m)

	clickAccordionZone(m, 0, row)

	if m.selection.selected() {
		t.Fatal("clicking the accordion zone left a text selection")
	}
	if got := accordionDocumentLines(m); got <= compact {
		t.Fatalf("click did not expand the block: document lines = %d, want more than %d", got, compact)
	}

	clickAccordionZone(m, 0, row)
	if got := accordionDocumentLines(m); got != compact {
		t.Fatalf("second click did not re-collapse the block: document lines = %d, want %d", got, compact)
	}
}

func TestDragFromAccordionRowSelectsWithoutToggling(t *testing.T) {
	m := accordionTestModel(t)
	full := accordionDocumentLines(m)
	row := accordionRow(t, m)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: row})
	m.Update(tea.MouseMotionMsg{Button: tea.MouseLeft, X: 20, Y: row + 1})
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 20, Y: row + 1})

	if !m.selection.selected() {
		t.Fatal("dragging from the accordion row did not select text")
	}
	if got := accordionDocumentLines(m); got != full {
		t.Fatalf("a drag toggled the block: document lines = %d, want %d", got, full)
	}
}

// TestCtrlOTogglesInEveryChatFocus checks ctrl+o reaches the transcript
// whichever chat surface owns input, without disturbing that surface.
func TestCtrlOTogglesInEveryChatFocus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(m *Model)
		keeps func(m *Model) bool
	}{
		{"editor", func(*Model) {}, func(m *Model) bool { return m.focus() == focusEditor }},
		{
			"completion menu",
			func(m *Model) { m.editor.Update(tea.PasteMsg{Content: "/"}) },
			func(m *Model) bool { return m.editor.MenuOpen() },
		},
		{
			"approval",
			func(m *Model) { m.syncApprovals([]agent.Block{accordionTestBlock()}) },
			func(m *Model) bool { return m.focus() == focusPrompt },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := accordionTestModel(t)
			tc.setup(m)
			m.relayout() // the setup may dock a surface, which changes the header
			compact := accordionDocumentLines(m)
			if !tc.keeps(m) {
				t.Fatal("fixture did not reach the focus under test")
			}

			m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			if got := accordionDocumentLines(m); got <= compact {
				t.Fatalf("ctrl+o did not expand: document lines = %d, want more than %d", got, compact)
			}
			if !tc.keeps(m) {
				t.Fatal("ctrl+o disturbed the surface that owns input")
			}

			m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			if got := accordionDocumentLines(m); got != compact {
				t.Fatalf("second ctrl+o did not re-collapse: document lines = %d, want %d", got, compact)
			}
		})
	}
}

// rawSequence extracts the string written by a tea.Raw command in cmd,
// unwrapping tea.Batch. It returns "" if cmd carries no RawMsg — Update
// batches reconcilePointerShape's result together with other reconcilers,
// most of which return nil on a given tick.
func rawSequence(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	switch msg := cmd().(type) {
	case tea.RawMsg:
		return fmt.Sprint(msg.Msg)
	case tea.BatchMsg:
		for _, c := range msg {
			if s := rawSequence(c); s != "" {
				return s
			}
		}
	}
	return ""
}

func TestMouseMotionSetsPointerShapeOverAnAccordionRow(t *testing.T) {
	m := accordionTestModel(t)
	row := accordionRow(t, m)

	_, cmd := m.Update(tea.MouseMotionMsg{X: 0, Y: row})
	if got := rawSequence(cmd); got != ansi.SetPointerShape("pointer") {
		t.Fatalf("hovering the row did not set the pointer shape: got %q, want %q", got, ansi.SetPointerShape("pointer"))
	}

	_, cmd = m.Update(tea.MouseMotionMsg{X: 0, Y: row + 1})
	if got := rawSequence(cmd); got != ansi.SetPointerShape("default") {
		t.Fatalf("un-hovering the row did not reset the pointer shape: got %q, want %q", got, ansi.SetPointerShape("default"))
	}
}

// scrollableAccordionTestModel builds several identical tool blocks in a
// viewport too short to show them all, so ScrollBy actually moves the
// offset instead of no-oping at AtBottom().
func scrollableAccordionTestModel(t *testing.T) *Model {
	t.Helper()
	m := newShell()
	m.mode = ModeChat
	blocks := make([]agent.Block, 0, 6)
	for i := range 6 {
		blocks = append(blocks, agent.Block{
			ID:   agent.BlockID{Scope: agent.ScopeTool, Key: fmt.Sprintf("call-%d", i)},
			Kind: assistant.KindToolResult,
			Tool: &agent.ToolBlock{Name: "search_logs", Status: agent.ToolSuccess, Output: accordionTestOutput},
		})
	}
	m.transcript.Blocks = blocks
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 8})
	m.syncTranscript()
	m.editor.Focus()
	return m
}

// visibleAccordionRow locates the disclosure glyph's row within the current
// viewport (as opposed to accordionRow's full, unclipped Document scan) —
// needed once the list follows the bottom and the top block has scrolled
// off-screen.
func visibleAccordionRow(t *testing.T, m *Model) int {
	t.Helper()
	for i, line := range strings.Split(m.list.Render(), "\n") {
		if plain := ansi.Strip(line); strings.ContainsAny(plain, "▼▶") {
			return i
		}
	}
	t.Fatal("no accordion row visible in the viewport")
	return -1
}

func TestMouseWheelScrollRefreshesHoverUnderTheStationaryPointer(t *testing.T) {
	m := scrollableAccordionTestModel(t)
	// Expanded blocks put detail rows, not the next header, under the pointer
	// once the wheel scrolls.
	m.list.ToggleAllDisclosure()
	m.list.ScrollToTop()
	row := visibleAccordionRow(t, m)

	m.Update(tea.MouseMotionMsg{X: 0, Y: row})
	if !m.list.Hovered() {
		t.Fatal("expected hovering the chevron to set hover")
	}

	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 0, Y: row})
	if m.list.Hovered() {
		t.Fatal("scrolling moved the accordion row out from under the pointer, but hover was not recomputed")
	}
}

func TestResizeToTooSmallResetsPointerShape(t *testing.T) {
	m := scrollableAccordionTestModel(t)
	m.list.ScrollToTop()
	row := visibleAccordionRow(t, m)
	if _, cmd := m.Update(tea.MouseMotionMsg{X: 0, Y: row}); rawSequence(cmd) != ansi.SetPointerShape("pointer") {
		t.Fatal("hovering the row did not set the pointer shape")
	}

	// Only the height drops below the minimum, so the row under the pointer is
	// still a transcript header; the resize hint replacing the view must win.
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: minimumChatHeight - 1})
	if !m.frame.tooSmall || !m.list.Hovered() {
		t.Fatal("fixture must be too small while the list still reports hover")
	}
	if got := rawSequence(cmd); got != ansi.SetPointerShape("default") {
		t.Fatalf("resize to the too-small view did not reset the pointer shape: got %q", got)
	}
}

package tui

import (
	"fmt"
	"image"
	"reflect"
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

func TestCtrlOTogglesDisclosureEvenWhileCompletionMenuIsOpen(t *testing.T) {
	m := accordionTestModel(t)
	full := accordionDocumentLines(m)

	// The first ctrl+o is a no-op (expand-first alternation, see
	// TestCtrlOTogglesAllToolsWithExpandFirstAlternation); send it before
	// opening the menu so the one under test is the collapsing press.
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})

	m.editor.Update(tea.PasteMsg{Content: "/"})
	if !m.editor.MenuOpen() {
		t.Fatal("expected the completion menu to open on a leading slash")
	}

	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})

	if !m.editor.MenuOpen() {
		t.Fatal("ctrl+o must not close the completion menu")
	}
	if got := accordionDocumentLines(m); got >= full {
		t.Fatalf("ctrl+o with the menu open did not collapse the block: document lines = %d, want fewer than %d", got, full)
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
	clearMultiplexerEnv(t)
	m := newShell()
	m.mode = ModeChat
	blocks := make([]agent.Block, 0, 6)
	for i := 0; i < 6; i++ {
		blocks = append(blocks, agent.Block{
			ID:   agent.BlockID{Scope: agent.ScopeTool, Key: fmt.Sprintf("call-%d", i)},
			Kind: assistant.KindToolResult,
			Tool: &agent.ToolBlock{Name: "search_logs", Status: agent.ToolSuccess, Output: "ok: 4 results"},
		})
	}
	m.blocks = blocks
	m.resize(80, 8)
	m.syncTranscript()
	m.editor.Focus()
	m.list.Render()
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

func TestAdvanceSelectionScrollRefreshesHoverAtThePointer(t *testing.T) {
	m := scrollableAccordionTestModel(t)
	m.list.ScrollToTop()
	row := visibleAccordionRow(t, m)

	m.Update(tea.MouseMotionMsg{X: 0, Y: row})
	if !m.list.Hovered() {
		t.Fatal("expected hovering the chevron to set hover")
	}

	m.selection.pointer = image.Point{X: 0, Y: row}
	m.list.ScrollBy(1)
	m.refreshHover(m.selection.pointer.X, m.selection.pointer.Y)

	if m.list.Hovered() {
		t.Fatal("refreshHover did not clear hover after the row scrolled out from under the pointer")
	}
}

// sequencedCmds unwraps the batch of commands tea.Sequence produces. The
// message type it returns (sequenceMsg) is unexported, so it can only be
// inspected via reflection from outside package tea.
func sequencedCmds(t *testing.T, cmd tea.Cmd) []tea.Cmd {
	t.Helper()
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice {
		t.Fatalf("expected ctrl+c to return a sequenced command, got %T", msg)
	}
	cmds := make([]tea.Cmd, v.Len())
	for i := range cmds {
		c, ok := v.Index(i).Interface().(tea.Cmd)
		if !ok {
			t.Fatalf("sequence element %d was not a tea.Cmd", i)
		}
		cmds[i] = c
	}
	return cmds
}

func TestCtrlCResetsPointerShapeBeforeQuitting(t *testing.T) {
	m := accordionTestModel(t)
	row := accordionRow(t, m)

	_, cmd := m.Update(tea.MouseMotionMsg{X: 0, Y: row})
	if got := rawSequence(cmd); got != ansi.SetPointerShape("pointer") {
		t.Fatalf("hovering the row did not set the pointer shape: got %q", got)
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c returned no command")
	}

	cmds := sequencedCmds(t, cmd)
	if len(cmds) < 2 {
		t.Fatalf("ctrl+c command was not a sequence of at least 2 steps, got %d", len(cmds))
	}
	if got := rawSequence(cmds[0]); got != ansi.SetPointerShape("default") {
		t.Fatalf("first step of the ctrl+c sequence did not reset the pointer shape: got %q", got)
	}
	if _, ok := cmds[len(cmds)-1]().(tea.QuitMsg); !ok {
		t.Fatalf("last step of the ctrl+c sequence was not tea.Quit")
	}
}

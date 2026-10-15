package editor

import (
	"image"
	"slices"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/tui/components"
)

// maxExpandedChipRows bounds expanded chips so that, with the text's own cap,
// the composer fits ordinary terminals.
const maxExpandedChipRows = 4

// chipRow is the interaction state of the attachment chips drawn above the
// prompt. placed, top, and left describe the last render, which is what the
// user clicks on.
type chipRow struct {
	expanded bool
	cursor   int
	// pointer is the editor-local cell under the mouse.
	pointer image.Point
	placed  []components.PlacedChip
	top     int
	left    int
}

// ChipsFocused reports whether the keyboard is on an attachment chip. The
// parent then routes esc here instead of interrupting the turn.
func (e *Editor) ChipsFocused() bool { return e.focus == focusAttachments }

// SetPointer records the editor-local cell under the mouse so the chip
// beneath it is highlighted.
func (e *Editor) SetPointer(x, y int) {
	before, wasHovered := e.hoveredChip()
	e.chips.pointer = image.Pt(x, y)
	if after, hovered := e.hoveredChip(); hovered != wasHovered || after != before {
		e.invalidateBody()
	}
}

// ChipHovered reports whether the mouse rests on a clickable chip.
func (e *Editor) ChipHovered() bool {
	_, ok := e.hoveredChip()
	return ok
}

// Click handles a left click at an editor-local cell and reports whether it
// hit a chip: a chip is removed, "+N more" and "less" toggle the rows.
func (e *Editor) Click(x, y int) bool {
	e.renderView()
	index, ok := components.ChipAt(e.chips.placed, x-e.chips.left, y-e.chips.top)
	if !ok {
		return false
	}
	if index == len(e.attachments) {
		e.changeChips(func() { e.chips.expanded = !e.chips.expanded })
		return true
	}
	e.removeAttachment(index)
	return true
}

// changeChips applies a change to the chip rows. The editor is anchored to the
// bottom of the screen, so when its height changes the stored pointer cell no
// longer points at what is under the mouse: forget it until the mouse moves.
func (e *Editor) changeChips(change func()) {
	before := e.Height()
	change()
	e.invalidateBody()
	if e.Height() != before {
		e.chips.pointer = image.Pt(-1, -1)
	}
}

func (e *Editor) hoveredChip() (int, bool) {
	p := e.chips.pointer
	return components.ChipAt(e.chips.placed, p.X-e.chips.left, p.Y-e.chips.top)
}

// chipKey handles keyboard access to chips and reports whether it consumed
// key. Backspace at the very start of the prompt moves onto the last chip;
// from there ←/→ move, backspace/delete remove, and esc returns to the text.
// Any other key returns to the text and is handled there.
func (e *Editor) chipKey(key string) bool {
	if e.focus != focusAttachments {
		if key == "backspace" && len(e.attachments) > 0 && e.ta.Focused() && !e.menu.open && e.cursorOffset() == 0 {
			e.focusChip(len(e.attachments) - 1)
			return true
		}
		return false
	}
	switch key {
	case "left":
		e.focusChip(max(0, e.chips.cursor-1))
		return true
	case "right":
		if e.chips.cursor < len(e.attachments)-1 {
			e.focusChip(e.chips.cursor + 1)
		} else {
			e.focus = focusText
		}
		e.invalidateBody()
		return true
	case "backspace", "delete":
		e.removeAttachment(e.chips.cursor)
		return true
	case "esc", "enter":
		e.focus = focusText
		e.invalidateBody()
		return true
	}
	e.focus = focusText
	e.invalidateBody()
	return false
}

// focusChip puts the keyboard on attachments[i], expanding the row when the
// chip is hidden behind "+N more".
func (e *Editor) focusChip(i int) {
	e.renderView()
	hidden := !slices.ContainsFunc(e.chips.placed, func(p components.PlacedChip) bool { return p.Index == i })
	e.changeChips(func() {
		if hidden {
			e.chips.expanded = true
		}
		e.focus = focusAttachments
		e.chips.cursor = i
	})
}

// removeAttachment drops attachments[i]. A focused chip hands focus to its
// left neighbour, and back to the text once no chip remains. Like a text edit,
// it ends history mode.
func (e *Editor) removeAttachment(i int) {
	e.history.end()
	e.changeChips(func() {
		e.attachments = slices.Delete(e.attachments, i, i+1)
		if e.chips.cursor >= i && e.chips.cursor > 0 {
			e.chips.cursor--
		}
		if len(e.attachments) == 0 {
			e.focus = focusText
			e.chips.expanded = false
		}
	})
}

// renderChips lays out and draws the chip rows, recording where each chip
// landed for hit-testing. animated tells whether the sweep row replaces the
// block's top border.
func (e *Editor) renderChips(animated bool) string {
	e.chips.placed = nil
	if len(e.attachments) == 0 {
		return ""
	}
	block := e.inputStyle.Block
	e.chips.top = block.GetBorderTopSize() + block.GetPaddingTop()
	if animated {
		e.chips.top = 1 + block.GetPaddingTop()
	}
	e.chips.left = block.GetBorderLeftSize() + e.ContentOffset()

	chips := make([]components.Chip, len(e.attachments))
	for i, a := range e.attachments {
		chips[i] = components.EntityChip(a.Type, a.ID, a.Label, true)
	}
	width := 80
	if e.width > 0 {
		width = e.width - block.GetHorizontalFrameSize() - e.inputStyle.PromptWidth()
	}
	sty := e.inputStyle.Chip
	layout := func(active int) []components.PlacedChip {
		if e.chips.expanded {
			return components.LayoutExpandedChips(sty, chips, width, maxExpandedChipRows, active)
		}
		return components.LayoutChips(sty, chips, width, 1, active)
	}
	e.chips.placed = layout(-1)
	active := -1
	if e.focus == focusAttachments {
		active = e.chips.cursor
	} else if i, ok := e.hoveredChip(); ok {
		active = i
	}
	if active >= 0 {
		e.chips.placed = layout(active)
	}

	indent := sty.Gap.Render(strings.Repeat(" ", e.inputStyle.PromptWidth()))
	lines := components.ChipRows(sty, e.chips.placed)
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

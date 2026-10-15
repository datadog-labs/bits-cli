package components

import (
	"image"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

// InputMessage is a read-only user prompt. Compact keeps the text on one line;
// attachments still use the same bounded chip row as submitted messages.
type InputMessage struct {
	Text              string
	Chips             []Chip
	Compact           bool
	Action            string // optional right-aligned control on the first text row
	ActionHighlighted bool
}

// InputMessageView includes the control's cells for callers that handle clicks.
type InputMessageView struct {
	View   string
	Action image.Rectangle
}

// RenderInputMessage shares the transcript's input surface with queued prompts.
// All text and chip labels are untrusted; Action is a caller-owned UI label.
func RenderInputMessage(message InputMessage, width int, sty styles.Input) InputMessageView {
	const minimumActionGap = 1
	inner := max(1, width-sty.Block.GetHorizontalFrameSize())
	markerWidth := sty.PromptWidth()
	indent := strings.Repeat(" ", markerWidth)
	var lines []string
	if len(message.Chips) > 0 {
		placed := LayoutChips(sty.Chip, message.Chips, max(1, inner-markerWidth), 1, -1)
		for _, row := range ChipRows(sty.Chip, placed) {
			lines = append(lines, sty.Chip.Gap.Render(indent)+row)
		}
		lines = append(lines, "")
	}
	textWidth := max(1, inner-markerWidth)
	actionWidth := ansi.StringWidth(message.Action)
	actionColumn := inner - actionWidth
	contentOrigin := image.Pt(sty.Block.GetBorderLeftSize()+sty.Block.GetPaddingLeft(), sty.Block.GetBorderTopSize()+sty.Block.GetPaddingTop())
	if actionWidth > 0 {
		textWidth = max(1, actionColumn-markerWidth-minimumActionGap)
	}
	text := escape.Multiline(message.Text)
	if message.Compact {
		text = ansi.Truncate(escape.SingleLine(message.Text), textWidth, "…")
	} else {
		text = ansi.Wordwrap(text, textWidth, "-")
	}
	var result InputMessageView
	for i, line := range strings.Split(text, "\n") {
		marker := sty.Text.Render(indent)
		if i == 0 {
			marker = sty.Marker.Render(sty.Prompt)
		}
		rendered := marker + sty.Text.Render(line)
		if i == 0 && actionWidth > 0 {
			actionStyle := sty.Placeholder
			if message.ActionHighlighted {
				actionStyle = sty.Marker
			}
			textEnd := markerWidth + ansi.StringWidth(line)
			actionStart := max(actionColumn, textEnd+minimumActionGap)
			gap := strings.Repeat(" ", actionStart-textEnd)
			rendered += sty.Text.Render(gap) + actionStyle.Render(message.Action)
			actionRow := len(lines)
			result.Action = image.Rect(actionStart, actionRow, actionStart+actionWidth, actionRow+1).Add(contentOrigin)
		}
		lines = append(lines, rendered)
	}
	result.View = sty.Block.Width(width).Render(strings.Join(lines, "\n"))
	return result
}

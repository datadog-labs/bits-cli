package chat

import (
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

func statusGlyph(state agent.ToolStatus, sty Styles, frame int) (string, lipgloss.Style) {
	switch state {
	case agent.ToolRunning:
		glyph := sty.StatusSpinner.Frame(frame)
		if glyph == "" {
			glyph = "•"
		}
		return glyph, sty.StatusRunning
	case agent.ToolAwaitingApproval:
		return "•", sty.StatusRunning
	case agent.ToolSuccess:
		return "✓", sty.StatusSuccess
	case agent.ToolError:
		return "✗", sty.StatusError
	default:
		return "•", sty.Meta
	}
}

// RenderActivityHeader renders the shared compact header used by thinking and
// grouped inspection activity and the background-run footer. Keeping the state-to-glyph mapping here makes
// every progressing row use the same spinner and static fallbacks.
func RenderActivityHeader(state agent.ToolStatus, label, suffix string, width int, sty Styles, frame int) string {
	glyph, glyphStyle := statusGlyph(state, sty, frame)
	header := glyphStyle.UnsetBackground().Render(glyph+" ") + sty.ToolName.Render(label)
	if suffix != "" {
		header += sty.ToolDetail.Render(suffix)
	}
	return ansi.Truncate(header, max(1, width), "…")
}

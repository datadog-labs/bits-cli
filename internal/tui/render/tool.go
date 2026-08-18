package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// toolOutputMaxLines caps how much tool output is shown before truncation.
// Full output / expansion (Ctrl+O) is deferred.
const toolOutputMaxLines = 12

// renderTool renders a tool call+result as one block: a header (name + status)
// and, when present, a one-line input summary and a truncated output body.
func renderTool(it chat.Item, width int, sty Styles) string {
	glyph, label, style := statusParts(it.Tool.Status, sty)

	header := style.Render(glyph+" ") + sty.ToolName.Render(toolName(it.Tool))
	if label != "" {
		header += sty.Meta.Render(" · ") + style.Render(label)
	}
	lines := []string{ansi.Truncate(header, width, "…")}

	if in := collapseWS(it.Tool.Input); in != "" {
		summary := ansi.Truncate(in, max(1, width-4), "…")
		lines = append(lines, sty.ToolDetail.Render("  ↳ "+summary))
	}
	if out := strings.TrimRight(it.Tool.Output, "\n"); out != "" {
		body := clampLines(wrap(out, max(1, width-2)), toolOutputMaxLines)
		lines = append(lines, indent(sty.ToolDetail.Render(body), 2))
	}
	return strings.Join(lines, "\n")
}

func toolName(t chat.ToolView) string {
	if t.Name == "" {
		return "tool"
	}
	return t.Name
}

// statusParts returns the glyph, label, and style for a tool status.
//
//exhaustive:enforce
func statusParts(s chat.ToolStatus, sty Styles) (glyph, label string, style lipgloss.Style) {
	switch s {
	case chat.ToolRunning:
		return "•", "running", sty.StatusRunning
	case chat.ToolSuccess:
		return "✓", "success", sty.StatusSuccess
	case chat.ToolError:
		return "✗", "error", sty.StatusError
	case chat.ToolUnknown:
		return "•", "", sty.Meta
	}
	return "•", "", sty.Meta
}

// collapseWS flattens runs of whitespace (including newlines) into single
// spaces, for a compact one-line input summary.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clampLines limits s to at most n lines, appending a "+N more" marker.
func clampLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	kept := append(lines[:n:n], fmt.Sprintf("… +%d more", len(lines)-n))
	return strings.Join(kept, "\n")
}

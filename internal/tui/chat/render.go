package chat

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// Render turns one transcript item into a styled block with no trailing newline
// (the caller joins items). width is the target display width in cells.
// Rendering is a single exhaustive switch over assistant.ContentKind so a new
// server content type fails the build until it is handled here.
func (it Item) Render(width int, sty Styles) string {
	switch it.Kind {
	case assistant.KindText:
		return renderText(it, width, sty)
	case assistant.KindReasoning:
		return renderReasoning(it, width, sty)
	case assistant.KindToolCall, assistant.KindToolResult:
		return renderTool(it, width, sty)
	case assistant.KindWidget, assistant.KindDashboard,
		assistant.KindProgress, assistant.KindTurnMarker,
		assistant.KindStop, assistant.KindInternal, assistant.KindUnknown:
		return fallback(it, width, sty)
	}
	return fallback(it, width, sty) // unreachable; the compiler needs a return
}

// fallback renders kinds without a dedicated renderer as a dim, bracketed label
// so nothing is silently dropped.
func fallback(it Item, width int, sty Styles) string {
	return sty.Meta.Render(wrap("["+it.Kind.String()+"]", width))
}

// renderText renders a user or assistant text fragment. Markdown is shown as
// plain wrapped text for now; rich markdown rendering is deferred.
func renderText(it Item, width int, sty Styles) string {
	if it.Role == assistant.RoleUser {
		return renderUser(it.Text, width, sty)
	}
	return sty.AssistantText.Render(wrap(it.Text, width))
}

// renderUser prefixes the first line with a marker and hangs the continuation
// lines under it so the message stays visually aligned.
func renderUser(text string, width int, sty Styles) string {
	const marker = "› "
	w := ansi.StringWidth(marker)

	lines := strings.Split(wrap(text, max(1, width-w)), "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = sty.UserMarker.Render(marker) + sty.UserText.Render(ln)
			continue
		}
		lines[i] = strings.Repeat(" ", w) + sty.UserText.Render(ln)
	}
	return strings.Join(lines, "\n")
}

// renderReasoning renders model thinking as dimmed, wrapped text. Collapsing
// (Ctrl+O) is deferred; the whole block is shown for now.
func renderReasoning(it Item, width int, sty Styles) string {
	return sty.Reasoning.Render(wrap(it.Text, width))
}

// toolOutputMaxLines caps how much tool output is shown before truncation.
// Full output / expansion (Ctrl+O) is deferred.
const toolOutputMaxLines = 12

// renderTool renders a tool call+result as one block: a header (name + status)
// and, when present, a one-line input summary and a truncated output body.
func renderTool(it Item, width int, sty Styles) string {
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

func toolName(t ToolView) string {
	if t.Name == "" {
		return "tool"
	}
	return t.Name
}

// statusParts returns the glyph, label, and style for a tool status.
func statusParts(s ToolStatus, sty Styles) (glyph, label string, style lipgloss.Style) {
	switch s {
	case ToolRunning:
		return "•", "running", sty.StatusRunning
	case ToolSuccess:
		return "✓", "success", sty.StatusSuccess
	case ToolError:
		return "✗", "error", sty.StatusError
	case ToolUnknown:
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

// wrap word-wraps s to width (min 1) without padding.
func wrap(s string, width int) string {
	if width < 1 {
		width = 1
	}
	return ansi.Wordwrap(s, width, "-")
}

// indent prefixes every line of s with pad spaces.
func indent(s string, pad int) string {
	if pad <= 0 || s == "" {
		return s
	}
	prefix := strings.Repeat(" ", pad)
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

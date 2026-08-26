package chat

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

// RenderBlock turns one aggregated block into a styled string with no trailing
// newline (the caller joins blocks). width is the target display width in cells.
// Rendering is a single exhaustive switch over assistant.ContentKind so a new
// server content type fails the build until it is handled here.
func RenderBlock(it agent.Block, width int, sty Styles) string {
	switch it.Kind {
	case assistant.KindText:
		return renderText(it, width, sty)
	case assistant.KindReasoning:
		return renderReasoning(it, width, sty)
	case assistant.KindToolCall, assistant.KindToolResult:
		return renderTool(it, width, sty)
	case assistant.KindWidget:
		return renderWidget(it, width, sty)
	case assistant.KindDashboard:
		return renderDashboard(it, width, sty)
	case assistant.KindProgress:
		return renderProgress(it, width, sty)
	case assistant.KindTurnMarker, assistant.KindStop,
		assistant.KindInternal, assistant.KindUnknown:
		return fallback(it, width, sty)
	}
	return fallback(it, width, sty) // unreachable; the compiler needs a return
}

// fallback renders kinds without a dedicated renderer as a dim, bracketed label
// so nothing is silently dropped.
func fallback(it agent.Block, width int, sty Styles) string {
	return sty.Meta.Render(wrap("["+it.Kind.String()+"]", width))
}

// renderText renders a user or assistant text fragment. Assistant text is
// rendered as markdown; user text keeps its marker and stays plain.
func renderText(it agent.Block, width int, sty Styles) string {
	text := it.Markdown.Content
	if it.Role == assistant.RoleUser {
		return renderUser(text, width, sty)
	}
	return renderMarkdown(text, width, sty.Markdown)
}

// renderUser draws the user prompt as a shared input block: a colored caret,
// the text hang-indented under it, all on the shared input background with one
// row of vertical padding above and below (from InputBlock). The caret and its
// single-space gutter match the editor prompt so a submitted message aligns
// exactly with what was typed.
func renderUser(text string, width int, sty Styles) string {
	marker := sty.Input.Prompt
	w := sty.Input.PromptWidth()

	// Reserve the block's horizontal padding, then the caret gutter, so wrapped
	// text fits inside the background fill.
	inner := max(1, width-sty.Input.Block.GetHorizontalFrameSize())

	lines := strings.Split(wrap(text, max(1, inner-w)), "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = sty.Input.Marker.Render(marker) + sty.Input.Text.Render(ln)
			continue
		}
		// Hang-indent continuation lines; the padding carries the background too.
		lines[i] = sty.Input.Text.Render(strings.Repeat(" ", w) + ln)
	}
	return sty.Input.Block.Width(width).Render(strings.Join(lines, "\n"))
}

// renderReasoning renders model thinking as dimmed, wrapped text. Redacted
// thinking has no body to show, so it renders a marker instead of nothing.
// Collapsing (Ctrl+O) is deferred; the whole block is shown for now.
func renderReasoning(it agent.Block, width int, sty Styles) string {
	text := it.Thinking.Content
	if it.Thinking.Redacted && text == "" {
		text = "[redacted]"
	}
	return sty.Reasoning.Render(wrap(text, width))
}

// renderWidget summarizes a widget block as a titled label; rich rendering of
// the visualization is deferred.
func renderWidget(it agent.Block, width int, sty Styles) string {
	label := "widget"
	if it.Widget != nil && it.Widget.Title != "" {
		label = "widget: " + it.Widget.Title
	}
	return sty.Meta.Render(wrap("["+label+"]", width))
}

// renderDashboard summarizes a dashboard block as a titled label; full
// dashboard rendering is deferred.
func renderDashboard(it agent.Block, width int, sty Styles) string {
	label := "dashboard"
	if it.Dashboard != nil && it.Dashboard.Title != "" {
		label = "dashboard: " + it.Dashboard.Title
	}
	return sty.Meta.Render(wrap("["+label+"]", width))
}

// renderProgress renders an async background-task update as dimmed text.
func renderProgress(it agent.Block, width int, sty Styles) string {
	if it.Progress == nil {
		return fallback(it, width, sty)
	}
	return sty.Meta.Render(wrap(it.Progress.Content, width))
}

// toolOutputMaxLines caps how much tool output is shown before truncation.
// Full output / expansion (Ctrl+O) is deferred.
const toolOutputMaxLines = 12

// renderTool renders a tool call+result as one block: a header (name + status)
// and, when present, a one-line input summary and a truncated output body.
func renderTool(it agent.Block, width int, sty Styles) string {
	tool := it.Tool
	glyph, label, style := statusParts(tool.Status, sty)

	header := style.UnsetBackground().Render(glyph+" ") + sty.ToolName.Render(toolName(tool))
	if label != "" {
		header += sty.Meta.Render(" · ") + pill(style, label)
	}
	lines := []string{ansi.Truncate(header, width, "…")}

	if in := collapseWS(tool.Input); in != "" {
		summary := ansi.Truncate(in, max(1, width-4), "…")
		lines = append(lines, sty.ToolDetail.Render("  ↳ "+summary))
	}
	if out := strings.TrimRight(tool.Output, "\n"); out != "" {
		body := clampLines(wrap(out, max(1, width-2)), toolOutputMaxLines)
		lines = append(lines, indent(sty.ToolDetail.Render(body), 2))
	}
	return strings.Join(lines, "\n")
}

func toolName(t *agent.ToolCall) string {
	if t.Name == "" {
		return "tool"
	}
	return t.Name
}

// Rounded pill caps (powerline). Require a Nerd/Powerline font to render;
// without one they show as missing-glyph boxes.
const (
	pillCapLeft  = "" //
	pillCapRight = "" //
)

// pill wraps text in rounded caps colored to the style's background, forming a
// rounded chip. If the style has no background set, text is rendered as-is.
func pill(s lipgloss.Style, text string) string {
	bg := s.GetBackground()
	if _, ok := bg.(lipgloss.NoColor); ok {
		return s.Render(text)
	}
	caps := lipgloss.NewStyle().Foreground(bg)
	return caps.Render(pillCapLeft) + s.Render(text) + caps.Render(pillCapRight)
}

// statusParts returns the glyph, label, and style for a tool status.
func statusParts(s agent.ToolStatus, sty Styles) (glyph, label string, style lipgloss.Style) {
	switch s {
	case agent.ToolRunning:
		return "•", "running", sty.StatusRunning
	case agent.ToolSuccess:
		return "✓", "success", sty.StatusSuccess
	case agent.ToolError:
		return "✗", "error", sty.StatusError
	case agent.ToolUnknown:
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

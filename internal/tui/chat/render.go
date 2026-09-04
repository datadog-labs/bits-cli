package chat

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// RenderBlock turns one aggregated block into a styled string with no trailing
// newline. It is the stateless entry point used by dev tooling; the transcript
// list keeps a blockRenderer so successive Markdown blocks reuse its setup.
// frame is the animation step for in-flight status chips; every other block
// kind ignores it.
func RenderBlock(it agent.Block, width int, sty Styles, frame int) string {
	var r blockRenderer
	return r.RenderBlock(it, width, sty, frame)
}

type blockRenderer struct {
	markdown markdownRenderer
}

// RenderBlock turns one aggregated block into a styled string with no trailing
// newline (the caller joins blocks). width is the target display width in cells,
// and frame is the animation step for in-flight status chips.
// Rendering is a single exhaustive switch over assistant.ContentKind so a new
// server content type fails the build until it is handled here.
func (r *blockRenderer) RenderBlock(it agent.Block, width int, sty Styles, frame int) string {
	switch it.Kind {
	case assistant.KindText:
		return r.renderText(it, width, sty)
	case assistant.KindReasoning:
		return renderReasoning(it, width, sty)
	case assistant.KindToolCall, assistant.KindToolResult:
		return renderTool(it, width, sty, frame)
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

func (r *blockRenderer) renderText(it agent.Block, width int, sty Styles) string {
	text := it.Markdown.Content
	if it.Role == assistant.RoleUser {
		return renderUser(text, width, sty)
	}
	return r.markdown.Render(text, width, sty.Markdown)
}

// renderUser draws the user prompt as a shared input block: a colored caret,
// the text hang-indented under it, all on the shared input background with one
// row of vertical padding above and below (from InputBlock). The caret and its
// single-space gutter match the editor prompt so a submitted message aligns
// exactly with what was typed.
func renderUser(text string, width int, sty Styles) string {
	text = escape.Multiline(text)
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
	text := escape.Multiline(it.Thinking.Content)
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
		label = "widget: " + escape.Inline(it.Widget.Title)
	}
	return sty.Meta.Render(wrap("["+label+"]", width))
}

// renderDashboard summarizes a dashboard block as a titled label; full
// dashboard rendering is deferred.
func renderDashboard(it agent.Block, width int, sty Styles) string {
	label := "dashboard"
	if it.Dashboard != nil && it.Dashboard.Title != "" {
		label = "dashboard: " + escape.Inline(it.Dashboard.Title)
	}
	return sty.Meta.Render(wrap("["+label+"]", width))
}

// renderProgress renders an async background-task update as dimmed text.
func renderProgress(it agent.Block, width int, sty Styles) string {
	if it.Progress == nil {
		return fallback(it, width, sty)
	}
	return sty.Meta.Render(wrap(escape.Multiline(it.Progress.Content), width))
}

// toolOutputMaxLines caps how much tool output is shown before truncation.
// Full output / expansion (Ctrl+O) is deferred.
const toolOutputMaxLines = 12

// renderTool renders a tool call+result as one block: a header (name + status)
// and, when present, a one-line input summary and a truncated output body.
func renderTool(it agent.Block, width int, sty Styles, frame int) string {
	tool := it.Tool
	chip := statusChipOf(tool.Status, sty)

	// Status leads the line: glyph, then the chip, then the tool name. A
	// succeeded tool has no chip at all — the check mark already says it
	// finished — so the name follows the glyph directly.
	header := chip.style.UnsetBackground().Render(chip.glyphAt(frame) + " ")
	if content := chip.content(frame); content != "" {
		header += pill(chip.style.GetBackground(), content) + " "
	}
	header += sty.ToolName.Render(toolName(tool))
	lines := []string{ansi.Truncate(header, width, "…")}

	if in := collapseWS(escape.Inline(tool.Input)); in != "" {
		summary := ansi.Truncate(in, max(1, width-4), "…")
		lines = append(lines, sty.ToolDetail.Render("  ↳ "+summary))
	}
	if out := strings.TrimRight(escape.Multiline(tool.Output), "\n"); out != "" {
		body := clampLines(wrap(out, max(1, width-2)), toolOutputMaxLines)
		lines = append(lines, indent(sty.ToolDetail.Render(body), 2))
	}
	return strings.Join(lines, "\n")
}

func toolName(t *agent.ToolBlock) string {
	if t.Name == "" {
		return "tool"
	}
	return escape.Inline(t.Name)
}

// Rounded pill caps (powerline). Require a Nerd/Powerline font to render;
// without one they show as missing-glyph boxes.
const (
	pillCapLeft  = "" //
	pillCapRight = "" //
)

// pill wraps already-styled chip content in rounded caps colored to the chip's
// background, forming a rounded chip. Content arrives pre-styled because an
// animated label carries its own per-character colors and must not be
// re-rendered through a single style. With no background there is no cap color
// to draw, so the content passes through unchanged.
func pill(bg color.Color, content string) string {
	if bg == nil {
		return content
	}
	if _, ok := bg.(lipgloss.NoColor); ok {
		return content
	}
	caps := lipgloss.NewStyle().Foreground(bg)
	return caps.Render(pillCapLeft) + content + caps.Render(pillCapRight)
}

// statusChip is the status portion of a tool header: a glyph, a style, and
// either a static label or a pre-rendered animated one.
type statusChip struct {
	glyph   string
	spinner styles.Spinner // animated glyph, replacing glyph while running
	label   string         // static label, for settled states
	anim    styles.Shimmer // animated label, for in-flight states
	style   lipgloss.Style
}

// glyphAt returns the chip's glyph at the given animation step: a spinner frame
// for a running tool, the static glyph for every other state.
func (c statusChip) glyphAt(frame int) string {
	if c.spinner.Len() > 0 {
		return c.spinner.Frame(frame)
	}
	return c.glyph
}

// content returns the chip's interior at the given animation step, already
// styled. An animated chip whose theme has been flattened (see
// Theme.WithoutMotion) still has a static rendering and no frames, so the
// degraded case needs no branch of its own here.
func (c statusChip) content(frame int) string {
	if c.anim.Len() > 0 {
		return c.anim.Frame(frame)
	}
	if static := c.anim.Static(); static != "" {
		return static
	}
	if c.label == "" {
		return ""
	}
	return c.style.Render(c.label)
}

// statusChipOf returns the chip for a tool status. The two in-flight states
// carry animated labels; settled ones are static so the transcript's render
// cache can keep serving them. Success has no label: the overwhelming majority
// of tool calls succeed, so labelling them adds noise the glyph already covers.
func statusChipOf(s agent.ToolStatus, sty Styles) statusChip {
	switch s {
	case agent.ToolRunning:
		// glyph is the fallback for a theme with motion disabled, where the
		// spinner has no frames; it matches the dot the other in-flight state
		// shows.
		return statusChip{glyph: "•", spinner: sty.StatusSpinner, anim: sty.StatusRunningLabel, style: sty.StatusRunning}
	case agent.ToolAwaitingApproval:
		// No spinner: the tool is blocked on the user, not making progress.
		return statusChip{glyph: "•", anim: sty.StatusAwaitingLabel, style: sty.StatusRunning}
	case agent.ToolSuccess:
		return statusChip{glyph: "✓", style: sty.StatusSuccess}
	case agent.ToolError:
		return statusChip{glyph: "✗", label: "error", style: sty.StatusError}
	case agent.ToolUnknown:
		return statusChip{glyph: "•", style: sty.Meta}
	}
	return statusChip{glyph: "•", style: sty.Meta}
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

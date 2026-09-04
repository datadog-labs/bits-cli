package chat

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/filediff"
	"github.com/DataDog/bits-cli/internal/tui/diffrender"
	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// toolOutputMaxLines caps generic output.
const toolOutputMaxLines = 12

// collapsedEditorDiffLines caps editor diffs.
const collapsedEditorDiffLines = 20

// renderTool renders one tool call/result.
func renderTool(it agent.Block, width int, sty Styles, frame int) string {
	tool := it.Tool
	if tool == nil {
		return fallback(it, width, sty)
	}

	switch tool.Name {
	case "write_file", "edit_file":
		if state, ok := tool.RenderState.(*filediff.State); ok && state != nil {
			return renderEditorTool(tool, state, width, sty, collapsedEditorDiffLines)
		}
		if diff, ok := filediff.ParseUnifiedDiff(tool.Detail); ok {
			return renderEditorDiff(tool, strings.TrimPrefix(diff.To, "b/"), &diff, "", width, sty, collapsedEditorDiffLines)
		}
	}

	return renderGenericTool(tool, width, sty, frame)
}

// renderGenericTool renders the generic tool view.
func renderGenericTool(tool *agent.ToolBlock, width int, sty Styles, frame int) string {
	chip := statusChipOf(tool.Status, sty)

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

// renderEditorTool renders live editor state.
func renderEditorTool(tool *agent.ToolBlock, state *filediff.State, width int, sty Styles, lineLimit int) string {
	path, diff, reason := editorDiff(state)
	return renderEditorDiff(tool, path, diff, reason, width, sty, lineLimit)
}

// renderEditorDiff renders live or restored diffs without reading the workspace.
func renderEditorDiff(tool *agent.ToolBlock, path string, diff *filediff.Diff, reason string, width int, sty Styles, lineLimit int) string {
	lines := []string{toolHeader(tool, width, sty)}
	if path != "" {
		lines = append(lines, sty.ToolDetail.Render("  ↳ "+ansi.Truncate(escape.Inline(path), max(1, width-4), "…")))
	}
	switch {
	case diff != nil:
		if format := formatChange(*diff); format != "" {
			lines = append(lines, sty.ToolDetail.Render("  ↳ "+format))
		}
		body := diffrender.Render(*diff, diffrender.Options{
			Path:     path,
			Width:    width,
			Style:    sty.Diff,
			MaxLines: lineLimit,
			Tail:     true,
		})
		if body != "" {
			lines = append(lines, body)
		}
	case reason != "":
		lines = append(lines, indent(sty.ToolDetail.Render(escape.Inline(reason)), 2))
	}
	return strings.Join(lines, "\n")
}

// editorDiff extracts live display data.
func editorDiff(state *filediff.State) (path string, diff *filediff.Diff, reason string) {
	switch {
	case state.Change != nil:
		return state.Change.Path, state.Change.Diff, state.Reason
	case state.Preview != nil:
		if state.Snapshot != nil {
			path = state.Snapshot.Path
		}
		return path, state.Preview.Diff, state.Reason
	case state.Snapshot != nil:
		return state.Snapshot.Path, nil, state.Reason
	default:
		return "", nil, state.Reason
	}
}

// formatChange reports encoding changes.
func formatChange(diff filediff.Diff) string {
	if diff.BeforeFormat == diff.AfterFormat {
		return ""
	}
	return "format: " + formatName(diff.BeforeFormat) + " → " + formatName(diff.AfterFormat)
}

func formatName(format filediff.TextFormat) string {
	name := strings.ToUpper(format.LineEnding)
	if format.BOM {
		name += " + BOM"
	}
	return name
}

// toolHeader renders a static editor-diff header.
func toolHeader(tool *agent.ToolBlock, width int, sty Styles) string {
	glyph, label, style := statusParts(tool.Status, sty)
	header := style.UnsetBackground().Render(glyph+" ") + sty.ToolName.Render(toolName(tool))
	if label != "" {
		header += sty.Meta.Render(" · ") + pill(style.GetBackground(), label)
	}
	return ansi.Truncate(header, width, "…")
}

func toolName(t *agent.ToolBlock) string {
	if t.Name == "" {
		return "tool"
	}
	return escape.Inline(t.Name)
}

// Powerline pill caps.
const (
	pillCapLeft  = "" //
	pillCapRight = "" //
)

// pill wraps chip content in colored caps.
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

type statusChip struct {
	glyph   string
	spinner styles.Spinner
	label   string
	anim    styles.Shimmer
	style   lipgloss.Style
}

func (c statusChip) glyphAt(frame int) string {
	if c.spinner.Len() > 0 {
		return c.spinner.Frame(frame)
	}
	return c.glyph
}

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

// statusChipOf returns the chip for a tool status.
func statusChipOf(s agent.ToolStatus, sty Styles) statusChip {
	switch s {
	case agent.ToolRunning:
		return statusChip{glyph: "•", spinner: sty.StatusSpinner, anim: sty.StatusRunningLabel, style: sty.StatusRunning}
	case agent.ToolAwaitingApproval:
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

// statusParts returns static chip values.
func statusParts(s agent.ToolStatus, sty Styles) (glyph, label string, style lipgloss.Style) {
	chip := statusChipOf(s, sty)
	label = chip.label
	if label == "" {
		label = chip.anim.Static()
	}
	return chip.glyph, label, chip.style
}

// collapseWS joins whitespace runs.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// clampLines truncates long output.
func clampLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	kept := append(lines[:n:n], fmt.Sprintf("… +%d more", len(lines)-n))
	return strings.Join(kept, "\n")
}

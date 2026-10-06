package chat

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

// itemSpacing describes outer rows owned by a rendered transcript item. The
// list collapses adjacent item spacing with its normal conversation gap.
type itemSpacing struct {
	before int
	after  int
}

// disclosure selects which view a disclosable renderer produces. Whether an
// item is disclosable at all is decided by List from its kind, before
// rendering.
type disclosure uint8

const (
	compactView disclosure = iota
	fullView
)

// renderContext carries the inputs shared by transcript block renderers. Its
// zero disclosure is compactView. Row-level helpers that lay out a narrower
// body keep taking an explicit width instead.
type renderContext struct {
	width      int
	sty        Styles
	frame      int
	disclosure disclosure
}

// RenderBlock renders a block without a trailing newline.
func RenderBlock(it agent.Block, width int, sty Styles, frame int) string {
	var r blockRenderer
	return r.RenderBlock(it, width, sty, frame)
}

type blockRenderer struct {
	markdown markdownRenderer
}

// RenderBlock renders a block without a trailing newline.
func (r *blockRenderer) RenderBlock(it agent.Block, width int, sty Styles, frame int) string {
	switch it.Kind {
	case assistant.KindText:
		return r.renderText(it, width, sty)
	case assistant.KindReasoning:
		return renderReasoning(it, width, sty, frame)
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
	return fallback(it, width, sty)
}

// fallback renders an unsupported block kind.
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

// renderUser renders the user prompt with a hanging indent.
func renderUser(text string, width int, sty Styles) string {
	text = escape.Multiline(text)
	marker := sty.Input.Prompt
	w := sty.Input.PromptWidth()

	inner := max(1, width-sty.Input.Block.GetHorizontalFrameSize())

	lines := strings.Split(wrap(text, max(1, inner-w)), "\n")
	for i, ln := range lines {
		if i == 0 {
			lines[i] = sty.Input.Marker.Render(marker) + sty.Input.Text.Render(ln)
			continue
		}
		lines[i] = sty.Input.Text.Render(strings.Repeat(" ", w) + ln)
	}
	return sty.Input.Block.Width(width).Render(strings.Join(lines, "\n"))
}

// renderReasoning renders model thinking in its compact, header-only view. An
// open block uses the same spinner as active tools and inspection groups; once
// closed it settles into a quiet completed row.
func renderReasoning(it agent.Block, width int, sty Styles, frame int) string {
	if it.Thinking == nil {
		return fallback(it, width, sty)
	}
	return renderReasoningGroup([]agent.Block{it}, renderContext{width: width, sty: sty, frame: frame})
}

// hasReasoningText reports whether a reasoning group has text to expand. It
// skips escaping to stay cheap, since List asks on every layout pass.
func hasReasoningText(blocks []agent.Block) bool {
	for _, block := range blocks {
		if block.Thinking != nil && strings.TrimSpace(block.Thinking.Content) != "" {
			return true
		}
	}
	return false
}

// renderReasoningGroup renders adjacent thinking blocks as one activity row.
// The compact view is the header alone; the full view adds the thinking text.
func renderReasoningGroup(blocks []agent.Block, c renderContext) string {
	state := agent.ToolSuccess
	label := "reasoning"
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if !block.Complete && state != agent.ToolRunning {
			state = agent.ToolRunning
			label = "reasoning" + activityEllipsis(c.frame, c.sty.StatusSpinner.Len() > 0)
		}
		if block.Thinking != nil {
			if text := strings.TrimSpace(escape.Multiline(block.Thinking.Content)); text != "" {
				parts = append(parts, text)
			}
		}
	}
	header := renderActivityHeader(state, label, "", c)
	if c.disclosure == compactView || len(parts) == 0 {
		return header
	}
	rows := wrappedRows(strings.Join(parts, "\n\n"), max(1, c.width-4))
	return header + "\n" + renderRows(rows, c.width, c.sty.ToolDetail, c.sty)
}

// renderWidget summarizes a widget.
func renderWidget(it agent.Block, width int, sty Styles) string {
	label := "widget"
	if it.Widget != nil && it.Widget.Title != "" {
		label = "widget: " + escape.Inline(it.Widget.Title)
	}
	return sty.Meta.Render(wrap("["+label+"]", width))
}

// renderDashboard summarizes a dashboard.
func renderDashboard(it agent.Block, width int, sty Styles) string {
	label := "dashboard"
	if it.Dashboard != nil && it.Dashboard.Title != "" {
		label = "dashboard: " + escape.Inline(it.Dashboard.Title)
	}
	return sty.Meta.Render(wrap("["+label+"]", width))
}

// renderProgress renders dimmed progress text.
func renderProgress(it agent.Block, width int, sty Styles) string {
	if it.Progress == nil {
		return fallback(it, width, sty)
	}
	return sty.Meta.Render(wrap(escape.Multiline(it.Progress.Content), width))
}

// wrap word-wraps s to width.
func wrap(s string, width int) string {
	if width < 1 {
		width = 1
	}
	return ansi.Wordwrap(s, width, "-")
}

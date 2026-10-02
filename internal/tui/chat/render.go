package chat

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/escape"
)

// itemSpacing describes outer rows owned by a rendered transcript item. The
// list collapses adjacent item spacing with its normal conversation gap.
type itemSpacing struct {
	before int
	after  int
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

// renderReasoning keeps model thinking compact until reasoning expansion is
// introduced. An open block uses the same spinner as active tools and
// inspection groups; once closed it settles into a quiet completed row.
func renderReasoning(it agent.Block, width int, sty Styles, frame int) string {
	if it.Thinking == nil {
		return fallback(it, width, sty)
	}
	return renderReasoningGroup([]agent.Block{it}, width, sty, frame)
}

func renderReasoningGroup(blocks []agent.Block, width int, sty Styles, frame int) string {
	state := agent.ToolSuccess
	label := "reasoning"
	for _, block := range blocks {
		if !block.Complete {
			state = agent.ToolRunning
			label = "thinking" + activityEllipsis(frame, sty.StatusSpinner.Len() > 0)
			break
		}
	}
	return renderActivityHeader(state, label, "", width, sty, frame)
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

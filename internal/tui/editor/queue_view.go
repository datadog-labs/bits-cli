package editor

import (
	"fmt"
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	maxQueueRows            = 11
	queuePromptGapRows      = 1
	queueFooterRows         = 1
	queueMinimumContentRows = 1
	queueMinimumWidth       = 6
)

type queueHit struct {
	rect  image.Rectangle
	index int // -1 is the overflow control
}

type queuePresentation struct {
	cursor   int
	pointer  image.Point
	hits     []queueHit
	nextPage int
}

// QueueView renders the unsent prompts separately from the draft so the parent
// can keep them visible even when an approval replaces the input box. The row
// budget includes overflow; queue growth never steals unbounded transcript rows.
func (e *Editor) QueueView(rows int) string {
	inputStyle := e.inputStyle
	inputStyle.Prompt = e.queueMarker()
	rendered := renderQueue(queueRenderInput{
		prompts:    e.queue.items,
		cursor:     e.queueUI.cursor,
		focused:    e.focus == focusQueue && e.Focused(),
		pointer:    e.queueUI.pointer,
		width:      e.width,
		rows:       rows,
		inputStyle: inputStyle,
		helpStyle:  e.styles.MenuHelp,
	})
	e.queueUI.hits = rendered.hits
	e.queueUI.nextPage = rendered.nextPage
	if len(rendered.hits) == 0 && e.focus == focusQueue {
		e.focus = focusText
		e.invalidateBody()
	}
	return rendered.view
}

// queueRendering keeps the painted view and its interaction geometry together.
// QueueView installs this result; rendering itself does not change editor state.
type queueRendering struct {
	view     string
	hits     []queueHit
	nextPage int
}

// queueRenderInput borrows prompts for synchronous, read-only rendering.
// The caller resolves animation and focus before rendering begins.
type queueRenderInput struct {
	prompts     []Prompt
	cursor      int
	focused     bool
	pointer     image.Point
	width, rows int
	inputStyle  styles.Input
	helpStyle   lipgloss.Style
}

func renderQueue(input queueRenderInput) queueRendering {
	var result queueRendering
	n := len(input.prompts)
	rows := min(input.rows, maxQueueRows)
	if n == 0 || rows <= 0 || input.width < queueMinimumWidth {
		return result
	}

	// One shared frame surrounds the stack. Blank surface rows separate
	// prompts; adjacent items never draw doubled borders.
	outer := input.inputStyle.Block
	if rows < outer.GetVerticalFrameSize()+queueMinimumContentRows {
		outer = outer.BorderTop(false).BorderBottom(false).PaddingTop(0).PaddingBottom(0)
	}
	innerWidth := input.width - outer.GetHorizontalFrameSize()
	origin := image.Pt(outer.GetBorderLeftSize()+outer.GetPaddingLeft(), outer.GetBorderTopSize()+outer.GetPaddingTop())
	budget := rows - outer.GetVerticalFrameSize()
	sty := input.inputStyle
	window := layoutQueue(input, innerWidth, budget)
	if window.overflows() && budget >= queueMinimumContentRows+queueFooterRows {
		// Reserve a footer only if the measured items actually overflow.
		window = layoutQueue(input, innerWidth, budget-queueFooterRows)
	}
	parts := make([]string, 0, len(window.items))
	for _, item := range window.items {
		hit := queueHit{rect: item.message.Action.Add(origin).Add(image.Pt(0, item.y)), index: item.index}
		result.hits = append(result.hits, hit)
		message := item.message
		if input.pointer.In(hit.rect) {
			message = renderQueuedPrompt(input.prompts[item.index], innerWidth, window.budget, sty, true)
		}
		parts = append(parts, message.View)
	}
	view := outer.Width(input.width).Render(strings.Join(parts, strings.Repeat("\n", queuePromptGapRows+1)))
	used := lipgloss.Height(view)
	if window.overflows() && used+queueFooterRows <= rows {
		label := fmt.Sprintf("  ↑ %d · ↓ %d · Tab to review", window.hiddenBefore, window.hiddenAfter)
		if input.focused {
			label = fmt.Sprintf("  ↑ %d · ↓ %d · Delete removes · Esc returns", window.hiddenBefore, window.hiddenAfter)
		}
		view += "\n" + input.helpStyle.Render(ansi.Truncate(label, input.width, "…"))
		result.hits = append(result.hits, queueHit{rect: image.Rect(0, used, input.width, used+queueFooterRows), index: -1})
		result.nextPage = window.end
		if window.end == n {
			result.nextPage = 0
		}
	}
	result.view = view
	return result
}

// queuedPlacement is measured content, before the shared frame is applied.
// The renderer's action rectangle follows that same translation as its pixels.
type queuedPlacement struct {
	index, y int
	message  components.InputMessageView
}

type queueWindow struct {
	end, budget               int
	hiddenBefore, hiddenAfter int
	items                     []queuedPlacement
}

func (w queueWindow) overflows() bool { return w.hiddenBefore > 0 || w.hiddenAfter > 0 }

// layoutQueue measures only the bounded window around the current selection.
// Heights come from the shared renderer, so chip or theme changes cannot make
// painting disagree with scrolling or hit-testing.
func layoutQueue(input queueRenderInput, width, budget int) queueWindow {
	views := make(map[int]components.InputMessageView)
	item := func(index int) components.InputMessageView {
		if view, ok := views[index]; ok {
			return view
		}
		highlighted := input.focused && input.cursor == index
		view := renderQueuedPrompt(input.prompts[index], width, budget, input.inputStyle, highlighted)
		views[index] = view
		return view
	}
	start := min(input.cursor, len(input.prompts)-1)
	used := lipgloss.Height(item(start).View)
	for start > 0 {
		height := lipgloss.Height(item(start - 1).View)
		if used+queuePromptGapRows+height > budget {
			break
		}
		start--
		used += queuePromptGapRows + height
	}
	window := queueWindow{hiddenBefore: start, end: start, budget: budget}
	used = 0
	for index := start; index < len(input.prompts); index++ {
		message := item(index)
		height := lipgloss.Height(message.View)
		gap := 0
		if len(window.items) > 0 {
			gap = queuePromptGapRows
		}
		if used+gap+height > budget {
			break
		}
		used += gap
		window.items = append(window.items, queuedPlacement{index: index, y: used, message: message})
		used += height
		window.end = index + 1
	}
	window.hiddenAfter = len(input.prompts) - window.end
	return window
}

func renderQueuedPrompt(p Prompt, width, budget int, sty styles.Input, highlighted bool) components.InputMessageView {
	// The stack owns the frame; individual messages own only their surface.
	sty.Block = lipgloss.NewStyle().Background(sty.Background)
	chips := make([]components.Chip, len(p.Attachments))
	for i, a := range p.Attachments {
		chips[i] = components.EntityChip(a.Type, a.ID, a.Label, false)
	}
	message := components.InputMessage{
		Text: p.Text, Chips: chips, Compact: true, Action: "× ",
		ActionHighlighted: highlighted,
	}
	rendered := components.RenderInputMessage(message, width, sty)
	if lipgloss.Height(rendered.View) > budget && len(chips) > 0 {
		// Preserve access to the prompt on short terminals. Put the count first
		// so truncating a long prompt cannot hide its attachment summary.
		message.Chips = nil
		message.Text = fmt.Sprintf("[%d attachments] %s", len(chips), p.Text)
		rendered = components.RenderInputMessage(message, width, sty)
	}
	return rendered
}

// queueMarker uses exactly the same spinner and timeline as tool activity.
// Waiting on the user and reduced-motion themes retain a static marker.
func (e *Editor) queueMarker() string {
	if e.QueuePaused() {
		return "Ⅱ "
	}
	if e.working && e.inputStyle.SweepMotion {
		if glyph := e.inputStyle.Spinner.Frame(e.activityFrame); glyph != "" {
			return glyph + " "
		}
	}
	return "◦ "
}

// SetQueuePointer uses coordinates local to the last QueueView.
func (e *Editor) SetQueuePointer(x, y int) { e.queueUI.pointer = image.Pt(x, y) }

func (e *Editor) QueueHovered() bool {
	for _, hit := range e.queueUI.hits {
		if e.queueUI.pointer.In(hit.rect) {
			return true
		}
	}
	return false
}

func (e *Editor) ClickQueue(x, y int) bool {
	for _, hit := range e.queueUI.hits {
		if !image.Pt(x, y).In(hit.rect) {
			continue
		}
		if hit.index < 0 {
			e.queueUI.cursor = e.queueUI.nextPage
		} else {
			e.RemoveQueued(hit.index)
		}
		return true
	}
	return false
}

func (e *Editor) ScrollQueue(delta int) {
	e.queueUI.cursor = max(0, min(e.QueueLen()-1, e.queueUI.cursor+delta))
	e.queueUI.pointer = image.Pt(-1, -1)
}

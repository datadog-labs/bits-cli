package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	// approvalCompactWidth is the slot width below which the panel switches to
	// condensed action labels so the choice row still fits.
	approvalCompactWidth = 50
	// approvalMinWidth is the narrowest slot that still shows the actions that
	// never drop (Allow, Deny). Below it the panel conceals entirely rather
	// than showing a partial choice row.
	approvalMinWidth = 36

	// fullAccessDisclaimer is the warning shown while the full-access choice
	// is focused. Verbatim from the design mockup.
	fullAccessDisclaimer = "Full access skips all approval requests. Use with caution."
)

type approvalChoice struct {
	decision     agent.ApprovalDecision
	label        string
	emphasis     string // trailing segment rendered bold, e.g. "full access"
	compactLabel string
	fullAccess   bool
	// dropPriority orders which choices give way first on a narrow slot: 0
	// never drops (Allow, Deny), higher drops before lower when the full
	// choice row does not fit. See actions.
	dropPriority int
}

var approvalChoices = [...]approvalChoice{
	{decision: agent.ApprovalAllowOnce, label: "Allow", compactLabel: "Allow"},
	{decision: agent.ApprovalAllowSession, label: "Allow for session", compactLabel: "Session", dropPriority: 1},
	// ApprovalAllowOnce, not ApprovalAllowSession: a session grant would be
	// silently cleared once the mode leaves manual, which would surprise the
	// user. Switching to full access answers only this call.
	{decision: agent.ApprovalAllowOnce, label: "Switch to ", emphasis: "full access", compactLabel: "Full", fullAccess: true, dropPriority: 2},
	{decision: agent.ApprovalDeny, label: "Deny", compactLabel: "Deny"},
}

// chip renders one choice's action label as a single fixed-background cell.
// The bold emphasis is a separate rendered segment deliberately: nesting
// Bold(true) inside the outer style resets the background for the rest of
// the row once the inner style's reset code fires.
func (c approvalChoice) chip(style lipgloss.Style, compact bool) string {
	if compact {
		return style.Render(c.compactLabel)
	}
	inner := style.UnsetPadding()
	return inner.PaddingLeft(1).Render(c.label) +
		inner.Bold(true).Render(c.emphasis) +
		inner.PaddingRight(1).Render("")
}

// approvalPrompt is the panel asking the user to allow one tool call. It owns
// the highlighted action, the body scroll, and the decision; its approvalAsk
// delivers that. A new prompt starts on the first action, scrolled to the top.
type approvalPrompt struct {
	block      agent.Block
	selected   int                    // index into approvalChoices
	visible    []int                  // indices into approvalChoices the last actions() rendered; empty until the first Layout
	window     components.Window      // the body rows the last layout showed
	decision   agent.ApprovalDecision // "" until the user decides
	fullAccess bool                   // the committed decision also asked to switch to full access
	panel      components.Panel

	theme styles.Theme
	chat  chat.Styles
}

var _ components.Prompt = (*approvalPrompt)(nil)

func newApprovalPrompt(block agent.Block) *approvalPrompt {
	return &approvalPrompt{block: block}
}

// Placement docks the panel above the composer, which keeps the user's draft
// in sight while they decide.
func (a *approvalPrompt) Placement() components.Placement { return components.Docked }

func (a *approvalPrompt) SetStyles(theme styles.Theme) {
	a.theme, a.chat = theme, chat.StylesFor(theme)
	a.panel.SetStyles(theme.Approval.Panel)
}

// Update moves the highlight, scrolls the body, or makes the decision. The
// wheel scrolls the body; clicks are left to text selection.
func (a *approvalPrompt) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			a.scroll(-mouseWheelDelta)
		case tea.MouseWheelDown:
			a.scroll(mouseWheelDelta)
		default:
		}
		return nil, true
	case tea.KeyPressMsg:
		switch msg.String() {
		case "left", "shift+tab":
			a.selected = a.cycle(-1)
		case "right", "tab":
			a.selected = a.cycle(1)
		case "pgup":
			a.scroll(-a.window.PageSize())
		case "pgdown":
			a.scroll(a.window.PageSize())
		case "esc":
			a.decision = agent.ApprovalDeny
		case "enter":
			choice := approvalChoices[a.selected]
			a.decision, a.fullAccess = choice.decision, choice.fullAccess
		default:
			return nil, false
		}
		return nil, true
	}
	return nil, false
}

func (a *approvalPrompt) scroll(rows int) { a.window = a.window.Scrolled(rows) }

// cycle moves the focus by delta positions among the choices the last Layout
// actually showed (a.visible), so a hidden choice can never be focused — the
// focus-cycle safety bug this guards against is a user landing on, and
// enter-ing, a control they never saw (e.g. a silently-dropped "Allow for
// session"). Before the first Layout, visible is empty and the cycle falls
// back to the full array.
func (a *approvalPrompt) cycle(delta int) int {
	visible := a.visible
	if len(visible) == 0 {
		visible = make([]int, len(approvalChoices))
		for i := range visible {
			visible[i] = i
		}
	}
	pos := slices.Index(visible, a.selected)
	if pos < 0 {
		pos = 0
	}
	pos = (pos + delta + len(visible)) % len(visible)
	return visible[pos]
}

// Layout renders the panel within the slot. It is answerable in the full
// form, whose body scrolls, or in the compact one, which only shows when the
// whole request fits; never in the one-line fallback. Layout keeps the body
// window it showed, so scrolling clamps to what the last frame displayed.
func (a *approvalPrompt) Layout(slot components.Slot) (string, bool) {
	width := slot.Width
	block := a.block
	prompt := block.Tool.Approval
	title := "Run " + escape.Inline(block.Tool.Name) + "?"
	detail := ""
	if prompt != nil {
		if prompt.Title != "" {
			title = escape.Inline(prompt.Title)
		}
		detail = prompt.Detail
	}

	queue := "Permission Required"
	if slot.Waiting > 1 {
		queue += " · " + strconv.Itoa(slot.Waiting) + " waiting"
	}
	sty := a.theme.Approval
	content := components.PanelContent{
		Title:   queue,
		Dismiss: "ESC x",
		BodyHeader: func(width int) string {
			return sty.Text.Render(ansi.Wordwrap(title, width, "-"))
		},
		ScrollOffset: a.window.Offset,
		ScrollHint:   "pgup/pgdown scroll",
		ScrollableBody: func(width int) string {
			if rendered, ok := chat.RenderToolApproval(block.Tool, width, a.chat); ok {
				return rendered
			}
			return sty.Detail.Render(ansi.Wordwrap(escape.Inline(detail), width, "-"))
		},
		BodyFooter:    a.actions,
		BodyFooterGap: 1,
		CompactTitle:  queue,
		CompactBody: func(width int) string {
			// Wrapped, not truncated: a request that does not fit whole falls
			// back to the one-line form, which cannot be answered.
			lines := []string{sty.Text.Render(ansi.Wordwrap(title, width, "-"))}
			if rendered, ok := chat.RenderToolApproval(block.Tool, width, a.chat); ok {
				lines = append(lines, rendered)
			} else if detail != "" {
				lines = append(lines, sty.Detail.Render(ansi.Wordwrap(escape.Inline(detail), width, "-")))
			}
			lines = append(lines, "", a.actions(width))
			return strings.Join(lines, "\n")
		},
		TinyMessage: "Resize terminal to approve",
	}
	rendered, window, shown := a.panel.Layout(width, slot.Height, content)
	a.window = window
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, rendered), shown && width >= approvalMinWidth
}

// actions renders the choice row within width, measurement-driven rather
// than threshold-driven: it tries full labels, then condensed labels, then
// drops choices (by dropPriority, never Allow or Deny) until the row fits.
// This also fixes a latent bug: with four choices the full-label row no
// longer fits a handful of slots that used to silently shear off "Deny"
// before Panel ever asked whether the row fit.
//
// It also records which choices actually rendered, in a.visible, and clamps
// a.selected onto that set — toward Allow (index 0), never toward a
// neighbour — so Update's focus cycle (see cycle) can never land on a choice
// the user cannot see.
func (a *approvalPrompt) actions(width int) string {
	compact := width < approvalCompactWidth
	visible := allChoiceIndices()
	for {
		row := a.renderChoiceRow(visible, compact)
		if ansi.StringWidth(row) <= width {
			break
		}
		if !compact {
			compact = true
			continue
		}
		drop, ok := lowestDroppable(visible)
		if !ok {
			break
		}
		visible = slices.DeleteFunc(visible, func(i int) bool { return i == drop })
	}
	a.visible = visible
	if !slices.Contains(visible, a.selected) {
		a.selected = 0 // clamp toward Allow; a resize must never park the cursor on full access
	}

	row := ansi.Truncate(a.renderChoiceRow(visible, compact), max(1, width), "…")
	if approvalChoices[a.selected].fullAccess {
		// Decision: the disclaimer is exactly one row at every width, never
		// word-wrapped, so the panel's focused height grows by exactly one row.
		row += "\n" + a.theme.Approval.Disclaimer.Render(ansi.Truncate(fullAccessDisclaimer, max(1, width), "…"))
	}
	return row
}

func (a *approvalPrompt) renderChoiceRow(visible []int, compact bool) string {
	sty := a.theme.Approval
	rendered := make([]string, len(visible))
	for n, i := range visible {
		choice := approvalChoices[i]
		style := sty.Action
		if i == a.selected {
			if choice.fullAccess {
				style = sty.FullAccess
			} else {
				style = sty.Selected
			}
		}
		rendered[n] = choice.chip(style, compact)
	}
	return strings.Join(rendered, "  ")
}

func allChoiceIndices() []int {
	indices := make([]int, len(approvalChoices))
	for i := range indices {
		indices[i] = i
	}
	return indices
}

// lowestDroppable finds the still-visible choice with the lowest nonzero
// dropPriority — the next one to give way when the row does not fit. Allow
// and Deny (priority 0) are never returned.
func lowestDroppable(visible []int) (int, bool) {
	best, bestPriority := -1, 0
	for _, i := range visible {
		priority := approvalChoices[i].dropPriority
		if priority == 0 {
			continue
		}
		if best == -1 || priority < bestPriority {
			best, bestPriority = i, priority
		}
	}
	return best, best != -1
}

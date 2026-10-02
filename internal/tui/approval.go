package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/components"
	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

const (
	// approvalCompactWidth is the slot width below which the panel switches to
	// condensed action labels so the choice row still fits.
	approvalCompactWidth = 50
	// approvalMinWidth is the narrowest slot whose choice row shows every
	// action, even condensed.
	approvalMinWidth = 36
)

type approvalChoice struct {
	decision     agent.ApprovalDecision
	label        string
	compactLabel string
}

var approvalChoices = [...]approvalChoice{
	{decision: agent.ApprovalAllowOnce, label: "Allow", compactLabel: "Allow"},
	{decision: agent.ApprovalAllowSession, label: "Allow for session", compactLabel: "Session"},
	{decision: agent.ApprovalDeny, label: "Deny", compactLabel: "Deny"},
}

// approvalPrompt is the panel asking the user to allow one tool call. It owns
// the highlighted action, the body scroll, and the decision; its approvalAsk
// delivers that. A new prompt starts on the first action, scrolled to the top.
type approvalPrompt struct {
	block    agent.Block
	selected int                    // index into approvalChoices
	window   components.Window      // the body rows the last layout showed
	decision agent.ApprovalDecision // "" until the user decides
	panel    components.Panel

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
			a.selected = (a.selected + len(approvalChoices) - 1) % len(approvalChoices)
		case "right", "tab":
			a.selected = (a.selected + 1) % len(approvalChoices)
		case "pgup":
			a.scroll(-a.window.PageSize())
		case "pgdown":
			a.scroll(a.window.PageSize())
		case "esc":
			a.decision = agent.ApprovalDeny
		case "enter":
			a.decision = approvalChoices[a.selected].decision
		default:
			return nil, false
		}
		return nil, true
	}
	return nil, false
}

func (a *approvalPrompt) scroll(rows int) { a.window = a.window.Scrolled(rows) }

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

// actions renders the choice row within width, condensing labels on narrow
// terminals. The highlighted choice uses the interactive fill.
func (a *approvalPrompt) actions(width int) string {
	sty := a.theme.Approval
	rendered := make([]string, len(approvalChoices))
	for i, choice := range approvalChoices {
		label := choice.label
		if width < approvalCompactWidth {
			label = choice.compactLabel
		}
		if i == a.selected {
			rendered[i] = sty.Selected.Render(label)
		} else {
			rendered[i] = sty.Action.Render(label)
		}
	}
	return ansi.Truncate(strings.Join(rendered, "  "), max(1, width), "")
}

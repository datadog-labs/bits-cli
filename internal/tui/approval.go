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
	// approvalCompactWidth is the dock width below which the panel switches to
	// condensed action labels so the choice row still fits.
	approvalCompactWidth = 50
	// The compact panel needs this much room to show its heading, request
	// title, detail, and every action.
	approvalMinWidth  = 36
	approvalMinHeight = 6
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
// the highlighted action, the body scroll, and the decision until the model
// collects it. A new prompt starts on the first action, scrolled to the top.
type approvalPrompt struct {
	block    agent.Block
	waiting  func() int             // requests waiting on the user, this one included
	selected int                    // index into approvalChoices
	window   components.Window      // the body rows the last layout showed
	decision agent.ApprovalDecision // made but not yet collected; "" when none
	panel    components.Panel

	theme styles.Theme
	chat  chat.Styles
}

var _ components.Prompt = (*approvalPrompt)(nil)

// newApprovalPrompt asks about block; its header counts the model's requests.
func (m *Model) newApprovalPrompt(block agent.Block) *approvalPrompt {
	return &approvalPrompt{block: block, waiting: func() int { return len(m.requests) }}
}

func (a *approvalPrompt) SetStyles(theme styles.Theme) {
	a.theme, a.chat = theme, chat.StylesFor(theme)
	a.panel.SetStyles(theme.Approval.Panel)
}

// Result hands over the agent.ApprovalDecision the user made, once: the
// panel stays until the transcript drops the call, and must not decide twice.
func (a *approvalPrompt) Result() (any, bool) {
	decision := a.decision
	a.decision = ""
	return decision, decision != ""
}

func (a *approvalPrompt) MinSize() (int, int) { return approvalMinWidth, approvalMinHeight }

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

// Layout renders the panel within width×height.
// It is where the prompt learns how much of the body shows, so scrolling clamps
// to what the last frame displayed.
func (a *approvalPrompt) Layout(width, height int) string {
	block := a.block
	if block.Tool == nil {
		return ""
	}
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
	if count := a.waiting(); count > 1 {
		queue += " · " + strconv.Itoa(count) + " waiting"
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
			lines := []string{sty.Text.Render(ansi.Truncate(title, width, "…"))}
			if rendered, ok := chat.RenderToolApproval(block.Tool, width, a.chat); ok {
				lines = append(lines, rendered)
			} else if detail != "" {
				lines = append(lines, sty.Detail.Render(ansi.Truncate(escape.Inline(detail), width, "…")))
			}
			lines = append(lines, "", a.actions(width))
			return strings.Join(lines, "\n")
		},
		TinyMessage: "Resize terminal to approve",
	}
	rendered, window := a.panel.Layout(width, height, content)
	a.window = window
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, rendered)
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

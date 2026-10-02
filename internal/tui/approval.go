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

// approvalPrompt is the docked panel asking the user to allow a tool call. It
// owns everything about the prompt — the queue, the highlighted action, the
// body scroll and the decision until the model collects it — so the rest of
// the UI only feeds it the pending calls and its input. The zero value is an
// idle prompt.
type approvalPrompt struct {
	pending  []agent.Block          // tool calls awaiting a decision, oldest first
	selected int                    // index into approvalChoices
	window   components.Window      // the body rows the last layout showed
	decision agent.ApprovalDecision // made but not yet collected; "" when none
	panel    components.Panel

	theme styles.Theme
	chat  chat.Styles
}

var _ prompt = (*approvalPrompt)(nil)

func (a *approvalPrompt) setStyles(theme styles.Theme, chatStyles chat.Styles) {
	a.theme, a.chat = theme, chatStyles
	a.panel.SetStyles(theme.Approval.Panel)
}

// active reports whether a call awaits a decision.
func (a *approvalPrompt) active() bool { return len(a.pending) > 0 }

// callID is the call the next decision applies to.
func (a *approvalPrompt) callID() string { return a.pending[0].ToolCallID() }

// set replaces the pending calls. The highlighted action and scroll position
// are kept while the same call stays at the head of the queue, so a streaming
// update does not reset what the user is doing.
func (a *approvalPrompt) set(pending []agent.Block) {
	if len(pending) == 0 || !a.active() || pending[0].ToolCallID() != a.callID() {
		a.selected = 0
		a.window = components.Window{}
		a.decision = ""
	}
	a.pending = pending
}

// clear drops every pending call and returns the prompt to its initial state.
func (a *approvalPrompt) clear() { a.set(nil) }

// result hands over the decision the user made, once.
func (a *approvalPrompt) result() (agent.ApprovalDecision, bool) {
	decision := a.decision
	a.decision = ""
	return decision, decision != ""
}

func (a *approvalPrompt) minSize() (int, int) { return approvalMinWidth, approvalMinHeight }

// update moves the highlight, scrolls the body, or makes the decision. The
// wheel scrolls the body; clicks are left to text selection.
func (a *approvalPrompt) update(msg tea.Msg) (tea.Cmd, bool) {
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
		case "shift+pgup":
			a.scroll(-a.window.PageSize())
		case "shift+pgdown":
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

// layout renders the panel within width×height, or "" when nothing is pending.
// It is where the prompt learns how much of the body shows, so scrolling clamps
// to what the last frame displayed.
func (a *approvalPrompt) layout(width, height int) string {
	if !a.active() || a.pending[0].Tool == nil {
		return ""
	}

	block := a.pending[0]
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
	if count := len(a.pending); count > 1 {
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
		ScrollHint:   "shift+pgup/pgdown scroll",
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

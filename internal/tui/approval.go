package tui

import (
	"image"
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
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
)

const (
	// approvalMinWidth is the narrowest slot that still shows the actions that
	// never drop (Allow, Deny); below it the panel conceals entirely.
	approvalMinWidth = 36

	// fullAccessDisclaimer is the warning shown while full access is focused.
	fullAccessDisclaimer = "Full access skips all approval requests. Use with caution."
	// draftingHint replaces the disclaimer while the user writes a message.
	draftingHint = "tab to choose"
)

// approvalGrant is the permissions change a choice makes before it answers
// the call; the engine then releases every parked call the change covers.
type approvalGrant uint8

const (
	grantNone       approvalGrant = iota
	grantSession                  // stop asking for this call's approval key
	grantFullAccess               // switch to skip-permissions
)

// approvalChoice is one modal action: an answer for the call, plus the
// permissions change that comes with it.
type approvalChoice struct {
	decision     agent.ApprovalDecision
	grant        approvalGrant
	label        string
	emphasis     string // trailing segment rendered bold, e.g. "full access"
	compactLabel string
	// dropPriority orders which choices give way on a narrow slot; 0 never
	// drops. See actions.
	dropPriority int
}

var approvalChoices = [...]approvalChoice{
	{decision: agent.ApprovalAllowOnce, label: "Allow", compactLabel: "Allow"},
	{decision: agent.ApprovalAllowOnce, grant: grantSession, label: "Allow for session", compactLabel: "Session", dropPriority: 1},
	{decision: agent.ApprovalAllowOnce, grant: grantFullAccess, label: "Switch to ", emphasis: "full access", compactLabel: "Full", dropPriority: 2},
	{decision: agent.ApprovalDeny, label: "Deny", compactLabel: "Deny"},
}

func (c approvalChoice) fullAccess() bool { return c.grant == grantFullAccess }

// chip renders one choice's action label. The bold emphasis is a separate
// rendered segment because nesting Bold(true) inside the outer style resets
// its background for the rest of the row.
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
// delivers that. A new prompt starts scrolled to the top, on the first action.
//
// While the user drafts over it, the panel is not focused: nothing is
// highlighted and the composer keeps the text keys, Enter included; the
// prompt keeps tab, esc and paging, and tab picks a choice.
type approvalPrompt struct {
	block    agent.Block
	choices  components.ButtonRow   // selects among approvalChoices
	window   components.Window      // the body rows the last layout showed
	decision agent.ApprovalDecision // "" until the user decides
	grant    approvalGrant          // the permissions change made with the decision
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

// Update moves the highlight, scrolls the body, or makes the decision. Text,
// pastes, and while unfocused the composer's keys are left unused for the
// composer. The wheel scrolls the body; clicks are left to text selection.
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
		if pressed, used := a.choices.Update(msg); used {
			if pressed {
				choice := approvalChoices[a.choices.Selected]
				a.decision, a.grant = choice.decision, choice.grant
			}
			return nil, true
		}
		switch msg.String() {
		case "pgup":
			a.scroll(-a.window.PageSize())
		case "pgdown":
			a.scroll(a.window.PageSize())
		case "esc":
			a.decision = agent.ApprovalDeny
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
	width, focused := slot.Width, slot.Focused
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
	design := chat.ApprovalOf(block.Tool, a.chat)
	if design.Title != "" {
		title = escape.Inline(design.Title)
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
			return textwrap.WrapProseText(width, title, sty.Text)
		},
		ScrollOffset: a.window.Offset,
		ScrollHint:   "pgup/pgdown scroll",
		ScrollableBody: func(width int) string {
			if design.Render != nil {
				return design.Render(width)
			}
			return textwrap.WrapProseText(width, escape.Inline(detail), sty.Detail)
		},
		BodyFooter: func(width int) string {
			return a.actions(width, focused)
		},
		BodyFooterGap: 1,
		CompactTitle:  queue,
		CompactBody: func(width int) string {
			// Wrapped, not truncated: a request that does not fit whole falls
			// back to the one-line form, which cannot be answered.
			lines := []string{textwrap.WrapProseText(width, title, sty.Text)}
			if design.Render != nil {
				lines = append(lines, design.Render(width))
			} else if detail != "" {
				lines = append(lines, textwrap.WrapProseText(width, escape.Inline(detail), sty.Detail))
			}
			// No blank line before actions: it already ends in the
			// disclaimer-or-blank row, keeping this form's height unchanged.
			lines = append(lines, a.actions(width, focused))
			return strings.Join(lines, "\n")
		},
		TinyMessage: "Resize terminal to approve",
	}
	layout := a.panel.Layout(width, slot.Height, content)
	a.window = layout.Window
	// Centered by hand so the choices' offset is the one drawn, not a copy of
	// lipgloss.PlaceHorizontal's rounding.
	offset := image.Pt((width-lipgloss.Width(layout.View))/2, 0)
	// The panel may measure a form it then rejects, so the choices are
	// committed only for the form that showed; the one-line fallback shows
	// none.
	body := layout.Body.Add(offset)
	var placed []components.PlacedButton
	if body.Dx() > 0 {
		row := a.fitChoices(body.Dx())
		a.choices.Selected = row.selected
		// The choice row and its disclaimer row end the body in both forms
		// (see actions); a row truncated to fit loses the clicks it hides.
		origin := image.Pt(body.Min.X, body.Max.Y-2)
		_, rects := components.JoinButtons(a.choiceChips(row, focused))
		for n, rect := range rects {
			placed = append(placed, components.PlacedButton{Index: row.visible[n], At: rect.Add(origin).Intersect(body)})
		}
	}
	a.choices.Place(placed, focused)
	view := lipgloss.PlaceHorizontal(width, lipgloss.Left, lipgloss.NewStyle().MarginLeft(offset.X).Render(layout.View))
	return view, body.Dx() > 0 && width >= approvalMinWidth
}

// choiceRow is the choice row that fits a width: the visible choices, the
// focus clamped onto them, and whether labels are condensed.
type choiceRow struct {
	visible  []int
	selected int
	compact  bool
}

// fitChoices measures the choice row within width: full labels, then
// condensed, then dropping choices by dropPriority until it fits. It leaves
// the prompt untouched.
func (a *approvalPrompt) fitChoices(width int) choiceRow {
	row := choiceRow{visible: allChoiceIndices(), selected: a.choices.Selected}
	for ansi.StringWidth(a.renderChoiceRow(row, false)) > width {
		if !row.compact {
			row.compact = true
			continue
		}
		drop, ok := lowestDroppable(row.visible)
		if !ok {
			break
		}
		row.visible = slices.DeleteFunc(row.visible, func(i int) bool { return i == drop })
	}
	if !slices.Contains(row.visible, row.selected) {
		row.selected = 0 // clamp toward Allow; a resize must never park the cursor on full access
	}
	return row
}

// actions renders the choice row that fits width, then its disclaimer row,
// which tells a drafting user how to answer.
func (a *approvalPrompt) actions(width int, focused bool) string {
	choices := a.fitChoices(width)
	row := ansi.Truncate(a.renderChoiceRow(choices, focused), max(1, width), "…")
	// The disclaimer row is always present, blank when unfocused, so focusing
	// full access cannot change the panel's height. It costs nothing: the
	// approval Frame gives up a row of bottom padding for it (see
	// builder.go's approvalPanel).
	disclaimer := ""
	switch {
	case !focused:
		disclaimer = a.theme.Approval.Detail.Render(ansi.Truncate(draftingHint, max(1, width), "…"))
	case approvalChoices[choices.selected].fullAccess():
		disclaimer = a.theme.Approval.Disclaimer.Render(ansi.Truncate(fullAccessDisclaimer, max(1, width), "…"))
	}
	return row + "\n" + disclaimer
}

func (a *approvalPrompt) renderChoiceRow(row choiceRow, focused bool) string {
	view, _ := components.JoinButtons(a.choiceChips(row, focused))
	return view
}

func (a *approvalPrompt) choiceChips(row choiceRow, focused bool) []string {
	sty := a.theme.Approval
	rendered := make([]string, len(row.visible))
	for n, i := range row.visible {
		choice := approvalChoices[i]
		style := sty.Action
		if focused && i == row.selected {
			if choice.fullAccess() {
				style = sty.FullAccess
			} else {
				style = sty.Selected
			}
		}
		rendered[n] = choice.chip(style, row.compact)
	}
	return rendered
}

func allChoiceIndices() []int {
	indices := make([]int, len(approvalChoices))
	for i := range indices {
		indices[i] = i
	}
	return indices
}

// lowestDroppable returns the next choice to give way when the row does not
// fit. Priority 0 (Allow, Deny) never drops.
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

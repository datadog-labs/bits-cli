package tui

import (
	"image"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	followUpsPadding = 2
	followUpsTitle   = "Suggested next steps"
	followUpsHint    = "tab to choose"
	followUpsHelp    = "↑↓ choose · enter send · esc dismiss"
)

// followUps offers the prompts the assistant's latest suggest_follow_ups
// call suggests sending next. It docks above the composer in a panel like an
// approval's, without taking the keyboard: tab moves to it, and Enter sends
// a suggestion. Clicks follow an approval's: one selects, the next sends.
type followUps struct {
	block     agent.BlockID // the call whose suggestions show; zero for none
	prompts   []string
	selector  *components.Selector
	dismissed agent.BlockID // the call whose suggestions the user dismissed
	picked    string        // the suggestion to send, until the host takes it
	// focused, rows, and dismiss are as last laid out: whether the list held
	// the keyboard, the cells of the rows it drew from index first, and
	// those of "ESC x", empty when not drawn.
	focused bool
	rows    image.Rectangle
	first   int
	dismiss image.Rectangle
	panel   components.Panel
	theme   styles.Theme
}

var _ components.Prompt = (*followUps)(nil)

// followUpsPrompt returns the suggestions while the chat waits for the
// user's next message with an empty composer, or nil. Suggestions are sent
// as a new turn, so they wait for the queue too.
func (m *Model) followUpsPrompt() *followUps {
	f := &m.followUps
	if m.mode != ModeChat || m.op.busy() || m.state.ReadOnly || len(m.state.Queued) > 0 ||
		!m.editor.Empty() || f.block == (agent.BlockID{}) || f.block == f.dismissed {
		return nil
	}
	return f
}

// updateFollowUps applies msg to the suggestions and sends the one picked.
// A key they leave unused while focused goes to the composer, which takes
// the keyboard back, so pgup or ctrl+d still work.
func (m *Model) updateFollowUps(msg tea.Msg) (tea.Cmd, bool) {
	f := &m.followUps
	cmd, used := f.Update(msg)
	if f.picked != "" {
		in := agent.TurnInput{Message: f.picked}
		f.picked = ""
		cmd = tea.Batch(cmd, m.startTurn(m.engine.StartTurn, in))
	}
	if _, key := msg.(tea.KeyPressMsg); (key && !used && f.focused) || m.followUpsPrompt() == nil {
		m.promptFocused = false
	}
	return cmd, used
}

// sync adopts the latest suggestions in blocks.
func (f *followUps) sync(blocks []agent.Block) {
	if block, prompts := f.latest(blocks); block != f.block {
		choices := make([]components.Choice, len(prompts))
		for i, prompt := range prompts {
			choices[i].Label = escape.Inline(prompt)
		}
		f.block, f.prompts = block, prompts
		f.selector = components.NewSelector(choices, f.theme.Selector)
	}
}

// latest finds the newest suggestions made since the user's last message.
func (f *followUps) latest(blocks []agent.Block) (agent.BlockID, []string) {
	for i := len(blocks) - 1; i >= 0; i-- {
		block := &blocks[i]
		if block.Role == assistant.RoleUser {
			break
		}
		if block.Tool == nil || block.Tool.Identity() != spec.ServerSuggestFollowUps {
			continue
		}
		if block.ID == f.block {
			return f.block, f.prompts // a call's final input never changes
		}
		if prompts := spec.ParseFollowUps(block.Tool.Input); len(prompts) > 0 {
			return block.ID, prompts
		}
	}
	return agent.BlockID{}, nil
}

func (f *followUps) SetStyles(theme styles.Theme) {
	f.theme = theme
	f.panel.SetStyles(theme.Approval.Panel)
	if f.selector != nil {
		f.selector.SetStyles(theme.Selector)
	}
}

// Placement docks the suggestions so the composer stays in reach.
func (f *followUps) Placement() components.Placement { return components.Docked }

// Update moves among the suggestions and picks or dismisses them while they
// hold the keyboard. As on an approval, a click selects a suggestion, one on
// the selected suggestion of a focused list sends it, and one on "ESC x"
// dismisses them; a click that focuses the terminal cannot send a message.
// Text is left to the composer, so typing writes a message instead.
func (f *followUps) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		at := image.Pt(msg.X, msg.Y)
		switch {
		case msg.Button != tea.MouseLeft:
			return nil, false
		case at.In(f.dismiss):
			f.dismissed = f.block
		case at.In(f.rows):
			row := f.first + at.Y - f.rows.Min.Y
			if f.focused && row == f.selector.Index() {
				f.pick()
			} else {
				f.selector.SetIndex(row)
			}
		default:
			return nil, false
		}
		return nil, true
	case tea.KeyPressMsg:
		if !f.focused || f.rows.Empty() {
			return nil, false
		}
		switch msg.String() {
		case "up", "shift+tab":
			f.selector.UpdateKey("up")
		case "down", "tab":
			f.selector.UpdateKey("down")
		case "enter":
			f.pick()
		case "esc":
			f.dismissed = f.block
		default:
			return nil, false
		}
		return nil, true
	}
	return nil, false
}

func (f *followUps) pick() { f.picked = f.prompts[f.selector.Index()] }

// Layout draws the suggestions in a panel, as a plain list where the panel
// does not fit, or not at all where no row does. It is always answerable:
// suggestions never hide the chat.
func (f *followUps) Layout(slot components.Slot) (string, bool) {
	f.focused, f.rows, f.dismiss = slot.Focused, image.Rectangle{}, image.Rectangle{}
	f.selector.SetFocused(f.focused)
	hint := followUpsHint
	if f.focused {
		hint = followUpsHelp
	}
	content := components.PanelContent{
		Title:   followUpsTitle,
		Dismiss: "ESC x",
		Body: func(width, _ int) string {
			// The hint row ends the body, set apart as an approval's actions are.
			return f.selector.View(width) + "\n\n" + f.theme.Approval.Detail.Render(ansi.Truncate(hint, width, "…"))
		},
	}
	if !f.panel.Fits(slot.Width, slot.Height, content) {
		return f.list(slot, hint), true
	}
	layout := f.panel.Layout(slot.Width, slot.Height, content)
	// Centered by hand, as an approval is, so the hit areas are where it drew.
	offset := image.Pt((slot.Width-lipgloss.Width(layout.View))/2, 0)
	body := layout.Body.Add(offset)
	f.dismiss = layout.Dismiss.Add(offset)
	f.first, f.rows = 0, image.Rect(body.Min.X, body.Min.Y, body.Max.X, body.Min.Y+len(f.prompts))
	return lipgloss.PlaceHorizontal(slot.Width, lipgloss.Left, lipgloss.NewStyle().MarginLeft(offset.X).Render(layout.View)), true
}

// list draws a header and as many suggestions as fit without the panel's
// frame, or nothing when none does.
func (f *followUps) list(slot components.Slot, hint string) string {
	width := slot.Width - 2*followUpsPadding
	rows := min(len(f.prompts), slot.Height-1)
	if width < 1 || rows < 1 {
		return ""
	}
	header := f.theme.Text.Secondary.Render(followUpsTitle) + f.theme.Text.Tertiary.Render(" · "+hint)
	list, window := f.selector.ViewWindow(width, rows)
	f.first = window.Start
	f.rows = image.Rect(followUpsPadding, 1, followUpsPadding+width, 1+window.End-window.Start)
	view := ansi.Truncate(header, width, "…") + "\n" + list
	return lipgloss.NewStyle().Padding(0, followUpsPadding).Render(view)
}

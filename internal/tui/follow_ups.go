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
	followUpsTitle   = "Suggested"
	followUpsHint    = "tab to choose"
	followUpsHelp    = "↑↓ choose · enter send · esc dismiss"
)

// followUps offers the prompts the assistant's latest suggest_follow_ups
// call suggests sending next. It docks above the composer without taking the
// keyboard: tab moves to it, and Enter or a click sends a suggestion.
type followUps struct {
	block     agent.BlockID // the call whose suggestions show; zero for none
	prompts   []string
	selector  *components.Selector
	dismissed agent.BlockID // the call whose suggestions the user dismissed
	picked    string        // the suggestion to send, until the host takes it
	// focused and rows are as last laid out: whether the list held the
	// keyboard, and the cells of the rows it drew from index first.
	focused bool
	rows    image.Rectangle
	first   int
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
	if f.selector != nil {
		f.selector.SetStyles(theme.Selector)
	}
}

// Placement docks the suggestions so the composer stays in reach.
func (f *followUps) Placement() components.Placement { return components.Docked }

// Update moves among the suggestions and picks or dismisses them while they
// hold the keyboard. A click on one sends it either way. Text is left to the
// composer, so typing writes a message instead.
func (f *followUps) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		at := image.Pt(msg.X, msg.Y)
		if msg.Button != tea.MouseLeft || !at.In(f.rows) {
			return nil, false
		}
		f.selector.SetIndex(f.first + at.Y - f.rows.Min.Y)
		f.pick()
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

// Layout draws a header and as many suggestions as fit, or nothing when none
// does. It is always answerable: suggestions never hide the chat.
func (f *followUps) Layout(slot components.Slot) (string, bool) {
	f.focused, f.rows = slot.Focused, image.Rectangle{}
	width := slot.Width - 2*followUpsPadding
	rows := min(len(f.prompts), slot.Height-1)
	if width < 1 || rows < 1 {
		return "", true
	}
	hint := followUpsHint
	if f.focused {
		hint = followUpsHelp
	}
	header := f.theme.Text.Secondary.Render(followUpsTitle) + f.theme.Text.Tertiary.Render(" · "+hint)
	f.selector.SetFocused(f.focused)
	list, window := f.selector.ViewWindow(width, rows)
	f.first = window.Start
	f.rows = image.Rect(followUpsPadding, 1, followUpsPadding+width, 1+window.End-window.Start)
	view := ansi.Truncate(header, width, "…") + "\n" + list
	return lipgloss.NewStyle().Padding(0, followUpsPadding).Render(view), true
}

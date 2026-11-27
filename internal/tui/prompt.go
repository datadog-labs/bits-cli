package tui

import (
	"image"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/tui/components"
)

// minimumPromptRows is what a prompt may take even past half the free rows:
// enough for a scrolling approval body on a short terminal.
const minimumPromptRows = 15

// The chat shows one ask waiting on the user (see ask and reshow) through its
// components.Prompt, which owns the keyboard while it shows, unless the user
// drafts over it (see Model.promptFocused). Its Placement docks it above the
// composer, which stays visible, or puts it in the composer's place (see
// frame).

// prompt returns the shown prompt, or nil. A command prompt shows over an
// ask, which waits beneath it: the user just asked for the command, and only
// commands allowed during a turn, such as /permissions, can meet an ask.
func (m *Model) prompt() components.Prompt {
	switch {
	case m.commandPrompt != nil:
		return m.commandPrompt.form
	case m.shown != nil:
		return m.shown.prompt()
	}
	return nil
}

// commandPrompt asks for the answer a slash command needs. It belongs to no
// tool call: ending an operation leaves it open, and ctrl+x dismisses it.
type commandPrompt struct {
	form components.Prompt
	// finish reports whether the user answered or dismissed form and, if they
	// answered, runs the command with the answer.
	finish func() (tea.Cmd, bool)
}

// answeringPrompt is a prompt that reports its answer as a V.
type answeringPrompt[V any] interface {
	components.Prompt
	Result() (value V, dismissed, done bool)
}

// openCommandPrompt shows form until the user answers it, then runs submit
// with the answer unless they dismissed it.
func openCommandPrompt[V any](m *Model, form answeringPrompt[V], submit func(V) tea.Cmd) {
	form.SetStyles(m.styles)
	m.commandPrompt = &commandPrompt{form: form, finish: func() (tea.Cmd, bool) {
		value, dismissed, done := form.Result()
		if !done || dismissed {
			return nil, done
		}
		return submit(value), true
	}}
	m.editor.CloseMenu()
}

// updateCommandPrompt applies msg to the command prompt and closes it once
// answered, unless the answer opened another one.
func (m *Model) updateCommandPrompt(msg tea.Msg) (tea.Cmd, bool) {
	p := m.commandPrompt
	if p == nil {
		return nil, false
	}
	cmd, used := p.form.Update(msg)
	submitted, done := p.finish()
	if done && m.commandPrompt == p {
		m.commandPrompt = nil
	}
	return tea.Batch(cmd, submitted), used
}

// promptRows is the most rows a prompt may take out of the free rows above
// the composer: half of them, so the transcript keeps the other half, but at
// least minimumPromptRows, and always leaving the transcript a row.
func promptRows(free int) int {
	return max(0, min(free-1, max(minimumPromptRows, free/2)))
}

// atPoint moves a click or a wheel event to p, such as the cell of the
// surface it landed on; those are the pointer events a surface receives.
func atPoint(msg tea.MouseMsg, p image.Point) tea.Msg {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		msg.X, msg.Y = p.X, p.Y
		return msg
	case tea.MouseWheelMsg:
		msg.X, msg.Y = p.X, p.Y
		return msg
	}
	return msg
}

// pointAt is the screen cell of a pointer event.
func pointAt(msg tea.MouseMsg) image.Point {
	mouse := msg.Mouse()
	return image.Pt(mouse.X, mouse.Y)
}

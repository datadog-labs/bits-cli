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
// components.Prompt, which owns the keyboard while it shows. Its Placement
// docks it above the composer, which stays visible but inert, or puts it in
// the composer's place (see frame).

// prompt returns the shown prompt, or nil. An ask shows over a command prompt.
func (m *Model) prompt() components.Prompt {
	switch {
	case m.shown != nil:
		return m.shown.prompt()
	case m.commandPrompt != nil:
		return m.commandPrompt.form
	}
	return nil
}

// commandPrompt asks for the text a slash command needs. It belongs to no tool
// call: ending an operation leaves it open, and ctrl+x dismisses it.
type commandPrompt struct {
	form   *components.TextPrompt
	submit func(string) tea.Cmd
}

// openCommandPrompt shows form until the user answers it, then runs submit
// with the answer unless they dismissed it.
func (m *Model) openCommandPrompt(form *components.TextPrompt, submit func(string) tea.Cmd) {
	form.SetStyles(m.styles)
	m.commandPrompt = &commandPrompt{form: form, submit: submit}
	m.editor.CloseMenu()
}

// updateCommandPrompt applies msg to the command prompt and closes it once
// answered.
func (m *Model) updateCommandPrompt(msg tea.Msg) (tea.Cmd, bool) {
	p := m.commandPrompt
	if p == nil {
		return nil, false
	}
	cmd, used := p.form.Update(msg)
	value, dismissed, done := p.form.Result()
	if !done {
		return cmd, used
	}
	m.commandPrompt = nil
	if dismissed {
		return cmd, used
	}
	return tea.Batch(cmd, p.submit(value)), used
}

// promptRows is the most rows a prompt may take out of the free rows above
// the composer: half of them, so the transcript keeps the other half, but at
// least minimumPromptRows, and always leaving the transcript a row.
func promptRows(free int) int {
	return max(0, min(free-1, max(minimumPromptRows, free/2)))
}

// inPrompt moves a click or a wheel event into the prompt's coordinates; those
// are the pointer events a prompt receives.
func (f frame) inPrompt(msg tea.MouseMsg) tea.Msg {
	origin := f.prompt.Min
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		msg.X, msg.Y = msg.X-origin.X, msg.Y-origin.Y
		return msg
	case tea.MouseWheelMsg:
		msg.X, msg.Y = msg.X-origin.X, msg.Y-origin.Y
		return msg
	}
	return msg
}

// pointAt is the screen cell of a pointer event.
func pointAt(msg tea.MouseMsg) image.Point {
	mouse := msg.Mouse()
	return image.Pt(mouse.X, mouse.Y)
}

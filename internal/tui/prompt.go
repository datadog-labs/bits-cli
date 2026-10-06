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

// prompt returns the shown prompt, or nil.
func (m *Model) prompt() components.Prompt {
	if m.shown == nil {
		return nil
	}
	return m.shown.prompt()
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

package tui

import (
	"image"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/components"
)

const (
	// minimumTranscriptRows is the transcript height a docked prompt leaves so
	// the chat stays readable and scrollable while the user answers.
	minimumTranscriptRows = 5
	// minimumDockRows is what a prompt may take even when that leaves the
	// transcript fewer rows: enough for a scrolling approval body.
	minimumDockRows = 15
)

// The dock shows the first request waiting on the user (see request) as its
// components.Prompt. It sits between the transcript and the composer and owns
// the keyboard while it shows; the editor stays visible but inert.

// prompt returns the docked prompt, or nil.
func (m *Model) prompt() components.Prompt {
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[0].prompt
}

// dockRows is the most rows a prompt may take out of the free rows above the
// composer. The transcript always keeps a row, and keeps
// minimumTranscriptRows once the dock has minimumDockRows.
func dockRows(free int) int {
	return max(0, min(free-1, max(minimumDockRows, free-minimumTranscriptRows)))
}

// updatePrompt applies msg to the docked prompt, then hands on its answer.
func (m *Model) updatePrompt(msg tea.Msg) (tea.Cmd, bool) {
	if len(m.requests) == 0 {
		return nil, false
	}
	r := m.requests[0]
	cmd, used := r.prompt.Update(msg)
	if answer, done := r.prompt.Result(); done {
		m.answer(r, answer)
	}
	return cmd, used
}

// answer hands the user's answer to whoever asked. A tool UI is done once
// answered; an approval stays until the transcript drops the call.
func (m *Model) answer(r *request, answer any) {
	if r.ui != nil {
		r.ui.Respond(answer, nil)
		m.drop(func(other *request) bool { return other == r })
		return
	}
	m.engine.Decide(r.callID, answer.(agent.ApprovalDecision))
}

// inDock moves a click or a wheel event into the dock's coordinates; those
// are the pointer events a prompt receives.
func (f frame) inDock(msg tea.MouseMsg) tea.Msg {
	origin := f.dock.Min
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

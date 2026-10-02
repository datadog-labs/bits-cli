package tui

import (
	"image"

	tea "charm.land/bubbletea/v2"
)

const (
	// minimumTranscriptRows is the transcript height a docked prompt leaves so
	// the chat stays readable and scrollable while the user answers.
	minimumTranscriptRows = 5
	// minimumDockRows is what a prompt may take even when that leaves the
	// transcript fewer rows: enough for a scrolling approval body.
	minimumDockRows = 15
)

// prompt is a tool request waiting on the user: a tool approval or a tool's
// interactive UI. It docks between the transcript and the composer and owns
// the keyboard while it shows; the editor stays visible but inert. The model
// collects its answer after each update (see settlePrompt).
type prompt interface {
	// layout fits the prompt to width and at most height rows and renders it.
	layout(width, height int) string
	// minSize is the smallest dock the prompt can be answered in. Below it the
	// chat is hidden behind the resize hint, so a hidden choice is never made.
	minSize() (width, height int)
	// update applies a key or a pointer event in dock coordinates. It reports
	// whether the prompt used the event, so a click it ignores can start a text
	// selection instead.
	update(tea.Msg) (tea.Cmd, bool)
}

// prompt returns the docked prompt, or nil. Tool UIs come first: they answer
// a request a running tool is blocked on.
func (m *Model) prompt() prompt {
	if m.activeToolUI != nil {
		return m.activeToolUI
	}
	if m.approval.active() {
		return &m.approval
	}
	return nil
}

// dockRows is the most rows a prompt may take out of the free rows above the
// composer. The transcript always keeps a row, and keeps
// minimumTranscriptRows once the dock has minimumDockRows.
func dockRows(free int) int {
	return max(0, min(free-1, max(minimumDockRows, free-minimumTranscriptRows)))
}

// updatePrompt applies msg to the docked prompt, then hands on its answer.
func (m *Model) updatePrompt(msg tea.Msg) (tea.Cmd, bool) {
	p := m.prompt()
	if p == nil {
		return nil, false
	}
	cmd, used := p.update(msg)
	m.settlePrompt()
	return cmd, used
}

// settlePrompt hands an answered prompt's answer to whoever asked for it.
func (m *Model) settlePrompt() {
	if session := m.activeToolUI; session != nil {
		if answer, done := session.component.Result(); done {
			session.request.Respond(answer, nil)
			m.nextToolUI()
		}
		return
	}
	if decision, decided := m.approval.result(); decided {
		m.engine.Decide(m.approval.callID(), decision)
	}
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

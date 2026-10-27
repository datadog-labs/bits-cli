package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

// forkButtonState is the done row's fork-button press gesture. List resolves
// hit-testing (DoneButtonAt/HoveredDoneButton/DoneButtonAnchor) and paints
// hover/press; this only remembers what was pressed so a release can
// re-check it, the same division of labor as followControl.
type forkButtonState struct {
	pressed bool
	anchor  agent.BlockID
}

// forkButtonMouseEnabled reports whether the done row's fork button can
// receive pointer input right now. The button is only ever part of the
// rendered chat transcript: outside ModeChat (status, permissions, the
// resize hint), or with an overlay shown or the editor's menu open, the
// transcript is either not drawn or deliberately not driven by the pointer,
// same as followTarget's own guard. Both the click handler and the pointer
// shape reconciler must agree on this, or the cursor can promise a click the
// handler would then ignore.
func (m *Model) forkButtonMouseEnabled() bool {
	return m.mode == ModeChat && !m.frame.tooSmall && m.shown == nil && !m.editor.MenuOpen()
}

// handleForkButtonMouse drives the done row's fork button, copying
// handleFollowMouse's discipline: a click records the pressed target and
// clears any selection, and a release fires only when it lands back on that
// same target and the fork is still eligible — re-checked live, never
// trusted from the press, because a busy operation, a new response, or an
// opened picker can change that between press and release.
func (m *Model) handleForkButtonMouse(msg tea.Msg) (tea.Cmd, bool) {
	if !m.forkButtonMouseEnabled() {
		return nil, false
	}
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil, false
		}
		anchor, ok := m.list.DoneButtonAt(msg.X, msg.Y)
		if !ok {
			return nil, false
		}
		m.forkButton.pressed = true
		m.forkButton.anchor = anchor
		m.selection.clear()
		return nil, true
	case tea.MouseReleaseMsg:
		if msg.Button != tea.MouseLeft || !m.forkButton.pressed {
			return nil, false
		}
		pressedAnchor := m.forkButton.anchor
		m.forkButton.pressed = false
		anchor, ok := m.list.DoneButtonAt(msg.X, msg.Y)
		if !ok || anchor != pressedAnchor || !m.forkEligible() {
			return nil, true
		}
		return m.startFork("", m.blockMessageID(anchor)), true
	default:
		return nil, false
	}
}

// reconcileForkButton drops a stuck press and refreshes the button's painted
// state once per Update, the same way followOverlay clears pressed/hover when
// its control disappears. A press can outlive what made it valid: a reply can
// start, a picker can open, or the notice it was pressed on can scroll out of
// the rebuilt presentation entirely.
func (m *Model) reconcileForkButton() {
	if m.forkButton.pressed {
		if anchor, ok := m.list.DoneButtonAnchor(); !ok || anchor != m.forkButton.anchor || !m.forkEligible() {
			m.forkButton.pressed = false
		}
	}
	_, hovered := m.list.HoveredDoneButton()
	m.list.SetDoneButtonActive(m.forkButton.pressed || hovered)
}

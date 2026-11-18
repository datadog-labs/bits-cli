package tui

import (
	"image"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

// followOverlayRows places the control's bottom border in the composer gap.
// Its content row still floats over the transcript without changing layout.
const followOverlayRows = 1

// followControl owns the button gesture over the frame's follow target. The
// list owns scroll position and auto-follow.
type followControl struct {
	pressed image.Rectangle
	hover   bool
}

func (m *Model) followTarget() (string, image.Rectangle) {
	if m.mode != ModeChat || m.frame.tooSmall || m.prompt() != nil ||
		m.editor.MenuOpen() ||
		m.selection.selecting() || m.selection.selected() ||
		m.list.Height() <= 0 || m.list.Following() || m.list.AtBottom() {
		return "", image.Rectangle{}
	}
	labels := []string{"↓ Jump to latest", "↓ Latest"}
	if m.chatPhase == chat.PhaseStreaming && m.op.run != 0 {
		labels = []string{"New activity · ↓ Jump to latest", "Streaming · ↓ Latest", "New · ↓"}
	}
	for _, label := range labels {
		padded := " " + label + " "
		contentWidth := ansi.StringWidth(padded)
		// +2 for the rounded border's left and right edge columns.
		boxWidth := contentWidth + 2
		if boxWidth <= m.width {
			x := (m.width - boxWidth) / 2
			y := max(0, m.list.Height()-followOverlayRows)
			// The border adds a row above and below the content row.
			return padded, image.Rect(x, y-1, x+boxWidth, y+2)
		}
	}
	return "", image.Rectangle{}
}

// followOverlay floats the jump-to-latest control over the transcript and
// composer gap, the same way the completion menu overlays the editor.
func (m *Model) followOverlay(base string) string {
	area := m.frame.follow
	if area.Empty() {
		return base
	}
	marker := m.styles.Input.Marker
	borderColor := m.styles.FollowBorder
	style := lipgloss.NewStyle().
		Foreground(marker.GetForeground()).
		Border(lipgloss.RoundedBorder())
	if m.follow.pressed == area || m.follow.hover {
		borderColor = marker.GetForeground()
		style = style.Bold(true)
	}
	style = style.BorderForeground(borderColor)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(style.Render(m.frame.followLabel)).X(area.Min.X).Y(area.Min.Y).Z(1),
	).Render()
}

// handleFollowMouse presses the control and jumps on a release over the same
// target; the frame drops it whenever a menu or a prompt hides it.
func (m *Model) handleFollowMouse(msg tea.MouseMsg) bool {
	over, _ := m.frame.at(pointAt(msg))
	area := m.frame.follow
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft || over != regionFollow {
			return false
		}
		m.follow.pressed = area
		m.selection.clear()
		return true
	case tea.MouseMotionMsg:
		m.follow.hover = over == regionFollow
		return m.follow.hover || !m.follow.pressed.Empty()
	case tea.MouseReleaseMsg:
		if msg.Button != tea.MouseLeft || m.follow.pressed.Empty() {
			return false
		}
		pressed := m.follow.pressed
		m.follow.pressed = image.Rectangle{}
		if area == pressed && over == regionFollow {
			m.list.ScrollToBottom()
		}
		return true
	}
	return false
}

package tui

import (
	"image"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// followOverlayRows lifts the control so it floats over the
// transcript's last two rows instead of sitting in the layout flow.
const followOverlayRows = 2

// followControl owns the painted target and button gesture. The list owns
// scroll position and auto-follow.
type followControl struct {
	area    image.Rectangle
	pressed image.Rectangle
	hover   bool
}

func (m *Model) followTarget() (string, image.Rectangle) {
	if m.mode != ModeChat || m.frame.tooSmall || m.shown != nil ||
		m.editor.MenuOpen() ||
		m.selection.selecting() || m.selection.selected() ||
		m.list.Height() <= 0 || m.list.Following() || m.list.AtBottom() {
		return "", image.Rectangle{}
	}
	labels := []string{"↓ Jump to latest", "↓ Latest"}
	if m.chatPhase == chat.PhaseStreaming && m.op.events != nil {
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

// followOverlay floats the jump-to-latest control on top of the transcript's
// last two rows, the same way the completion menu overlays the editor.
func (m *Model) followOverlay(base string) string {
	label, area := m.followTarget()
	m.follow.area = area
	if area.Empty() {
		m.follow.pressed = image.Rectangle{}
		m.follow.hover = false
		return base
	}
	marker := m.styles.Input.Marker
	borderColor := m.styles.Panel.Frame.GetBorderTopForeground()
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
		lipgloss.NewLayer(style.Render(label)).X(area.Min.X).Y(area.Min.Y).Z(1),
	).Render()
}

// Recheck eligibility: a menu can hide the control between a painted
// frame and mouse release.
func (m *Model) handleFollowMouse(msg tea.Msg) bool {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return false
		}
		_, area := m.followTarget()
		if area.Empty() || area != m.follow.area || !image.Pt(msg.X, msg.Y).In(area) {
			return false
		}
		m.follow.pressed = area
		m.selection.clear()
		return true
	case tea.MouseMotionMsg:
		_, area := m.followTarget()
		m.follow.hover = !area.Empty() && image.Pt(msg.X, msg.Y).In(area)
		return m.follow.hover || !m.follow.pressed.Empty()
	case tea.MouseReleaseMsg:
		if msg.Button != tea.MouseLeft || m.follow.pressed.Empty() {
			return false
		}
		pressed := m.follow.pressed
		m.follow.pressed = image.Rectangle{}
		_, area := m.followTarget()
		if area == pressed && area == m.follow.area && image.Pt(msg.X, msg.Y).In(area) {
			m.list.ScrollToBottom()
			m.follow.area = image.Rectangle{}
		}
		return true
	}
	return false
}

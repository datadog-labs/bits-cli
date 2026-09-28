package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

var permissionModes = [...]agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions}

var permissionOptionDetails = [...]string{
	"Bits will ask for approval before making any changes in your workspace",
	"Use with caution: Bits will execute actions on your behalf",
}

const fullAccessConfirmation = "Enabling full access will automatically approve all actions without requiring confirmation."

func permissionModeLabel(mode agent.PermissionsMode) string {
	if mode == agent.ModeSkipPermissions {
		return "Full Access"
	}
	return "Ask for Approval"
}

func (m *Model) permissionsBusy() bool {
	return m.turnEvents != nil || m.cancelTurn != nil || m.chatPhase == chat.PhaseLoading || len(m.pendingApprovals) > 0
}

func (m *Model) switchPermissions(argument string) tea.Cmd {
	if m.tools == nil || m.engine == nil {
		return m.showNotice(notice(chat.NoticeError, nil, "This session has no tool permissions to configure."), 0)
	}
	current := m.tools.PermissionsMode()
	if argument == "" {
		selected := current
		if m.pendingPermissions != "" {
			selected = m.pendingPermissions
		}
		m.permissionChoice = 0
		if selected == agent.ModeSkipPermissions {
			m.permissionChoice = 1
		}
		m.permissionConfirm = false
		m.permissionAllow = true
		m.editor.CloseMenu()
		m.setMode(ModePermissions)
		return nil
	}
	if argument != string(agent.ModeManual) && argument != string(agent.ModeSkipPermissions) {
		return m.showNotice(notice(chat.NoticeError, nil, "Unknown permissions mode %q. Use manual or skip-permissions.", argument), 0)
	}
	mode := agent.PermissionsMode(argument)
	if mode == current {
		m.pendingPermissions = ""
		return nil
	}
	if mode == m.pendingPermissions {
		return nil
	}
	if current == agent.ModeManual && mode == agent.ModeSkipPermissions {
		m.permissionChoice = 1
		m.permissionConfirm = true
		m.permissionAllow = true
		m.editor.CloseMenu()
		m.setMode(ModePermissions)
		return nil
	}
	return m.applyPermissionsMode(mode)
}

func (m *Model) applyPermissionsMode(mode agent.PermissionsMode) tea.Cmd {
	if m.permissionsBusy() {
		m.pendingPermissions = mode
		m.setMode(ModeChat)
		m.clearNotice()
		return nil
	}
	if err := m.setPermissionsMode(mode); err != nil {
		m.setMode(ModeChat)
		if errors.Is(err, agent.ErrOperationActive) {
			return m.showNotice(notice(chat.NoticeWarn, nil, "Permissions are still changing. Try again after this response."), 0)
		}
		return m.showNotice(notice(chat.NoticeError, err, "Could not switch the permissions mode."), 0)
	}
	m.setMode(ModeChat)
	return nil
}

func (m *Model) setPermissionsMode(mode agent.PermissionsMode) error {
	if err := m.engine.SetPermissionsMode(m.tools, mode); err != nil {
		return err
	}
	m.pendingPermissions = ""
	m.syncStatus()
	m.clearNotice()
	return nil
}

func (m *Model) applyPendingPermissions() tea.Cmd {
	if m.pendingPermissions == "" {
		return nil
	}
	mode := m.pendingPermissions
	if err := m.setPermissionsMode(mode); err != nil {
		m.pendingPermissions = ""
		return m.showNotice(notice(chat.NoticeError, err, "Could not switch the permissions mode."), 0)
	}
	if m.mode == ModePermissions {
		m.permissionConfirm = false
		m.permissionChoice = 0
		if mode == agent.ModeSkipPermissions {
			m.permissionChoice = 1
		}
	}
	return nil
}

func (m *Model) updatePermissionsKey(msg tea.KeyPressMsg) tea.Cmd {
	if m.permissionsCompact() {
		if msg.String() == "esc" {
			m.setMode(ModeChat)
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		m.setMode(ModeChat)
	case "up", "down", "left", "right", "tab", "shift+tab":
		if m.permissionConfirm {
			m.permissionAllow = !m.permissionAllow
		} else {
			m.permissionChoice = 1 - m.permissionChoice
		}
	case "enter":
		if m.permissionConfirm {
			if !m.permissionAllow {
				m.setMode(ModeChat)
				return nil
			}
			if m.tools.PermissionsMode() != agent.ModeManual {
				m.setMode(ModeChat)
				return m.showNotice(notice(chat.NoticeWarn, nil, "Permissions changed while the picker was open. Open it again."), 0)
			}
			return m.applyPermissionsMode(agent.ModeSkipPermissions)
		}
		mode := permissionModes[m.permissionChoice]
		if mode == m.tools.PermissionsMode() {
			m.pendingPermissions = ""
			m.setMode(ModeChat)
			return nil
		}
		if mode == m.pendingPermissions {
			m.setMode(ModeChat)
			return nil
		}
		if mode == agent.ModeSkipPermissions {
			m.permissionConfirm = true
			m.permissionAllow = true
			return nil
		}
		return m.applyPermissionsMode(mode)
	}
	return nil
}

func (m *Model) permissionsView() string {
	style := m.styles.Editor
	panel := m.styles.Panel
	width := min(100, max(1, m.width-m.editor.ContentOffset()))
	inner := max(1, width-style.MenuFrame.GetHorizontalFrameSize()-2)
	line := func(value string, selected bool) string {
		rowStyle := style.MenuItem
		if selected {
			rowStyle = style.MenuSelected
		}
		value = ansi.Truncate(value, inner, "…")
		return rowStyle.Render(value + strings.Repeat(" ", max(0, inner-ansi.StringWidth(value))))
	}
	header := "Manage Bits Permissions"
	if m.permissionConfirm {
		header = "Full Access"
	}
	closeHint := "ESC x"
	header = ansi.Truncate(header, max(1, inner-ansi.StringWidth(closeHint)-1), "…")
	headerGap := strings.Repeat(" ", max(1, inner-ansi.StringWidth(header)-ansi.StringWidth(closeHint)))
	rows := []string{panel.Title.Render(header) + headerGap + panel.Dismiss.Render(closeHint)}
	if m.permissionsCompact() {
		rows = append(rows, line("Resize terminal to choose permissions", false))
	} else if m.permissionConfirm {
		rows = append(rows, line("", false))
		for _, text := range wrapPermissionDetail(fullAccessConfirmation, inner) {
			rows = append(rows, line(text, false))
		}
		rows = append(rows,
			line("", false),
			line(permissionMarker(m.permissionAllow)+"Yes, enable full access", m.permissionAllow),
			line("", false),
			line(permissionMarker(!m.permissionAllow)+"Cancel", !m.permissionAllow),
		)
	} else {
		labels := m.permissionOptionLabels()
		leftWidth := permissionLabelWidth()
		detailWidth := inner - leftWidth - 2
		rows = append(rows,
			line("", false),
		)
		for i, label := range labels {
			if i > 0 {
				rows = append(rows, line("", false))
			}
			rows = append(rows, m.permissionOptionRows(label, permissionOptionDetails[i], inner, leftWidth, detailWidth, i == m.permissionChoice)...)
		}
	}
	if m.permissionsBusy() {
		timing := "Changes apply after this response."
		if m.pendingPermissions != "" {
			timing = permissionModeLabel(m.pendingPermissions) + " will apply after this response."
		}
		rows = append(rows, line("", false), line(timing, false))
	}
	rows = append(rows, line("", false))
	return style.MenuFrame.Width(width).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func (m *Model) permissionOptionLabels() [2]string {
	labels := [2]string{"Ask for Approval", "Full Access"}
	if m.tools.PermissionsMode() == agent.ModeManual {
		labels[0] += " (current)"
	} else {
		labels[1] += " (current)"
	}
	if m.pendingPermissions == agent.ModeManual {
		labels[0] += " (queued)"
	} else if m.pendingPermissions == agent.ModeSkipPermissions {
		labels[1] += " (queued)"
	}
	return labels
}

// Keep the detail column fixed when the current or queued suffix moves
// between options, so wrapping and vertical spacing do not jump.
func permissionLabelWidth() int {
	return ansi.StringWidth("› Ask for Approval (current)") + 2
}

func (m *Model) permissionOptionRows(label, detail string, width, leftWidth, detailWidth int, selected bool) []string {
	labelStyle, detailStyle := m.styles.Text.Secondary, m.styles.Text.Tertiary
	if selected {
		labelStyle = m.styles.Selector.Selected
		detailStyle = m.styles.Selector.Selected
	}
	wrapped := wrapPermissionDetail(detail, detailWidth)
	rows := make([]string, 0, len(wrapped))
	for i, part := range wrapped {
		left := strings.Repeat(" ", leftWidth)
		if i == 0 {
			left = permissionMarker(selected) + label
			left += strings.Repeat(" ", max(0, leftWidth-ansi.StringWidth(left)))
		}
		row := labelStyle.Render(left) + "  " + detailStyle.Render(part)
		rows = append(rows, row+strings.Repeat(" ", max(0, width-ansi.StringWidth(row))))
	}
	return rows
}

func wrapPermissionDetail(value string, width int) []string {
	width = max(1, width)
	var lines []string
	current := ""
	for _, word := range strings.Fields(value) {
		if current != "" && ansi.StringWidth(current)+1+ansi.StringWidth(word) > width {
			lines = append(lines, current)
			current = ""
		}
		if current != "" {
			current += " "
		}
		current += word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func permissionMarker(selected bool) string {
	if selected {
		return "› "
	}
	return "  "
}

func (m *Model) permissionsCompact() bool {
	available := m.height - chatFooterHeight - m.editor.Height()
	if m.permissionsBusy() {
		available -= 2 // timing line and its gap
	}
	if m.permissionConfirm {
		width := min(100, max(1, m.width-m.editor.ContentOffset()))
		inner := max(1, width-m.styles.Editor.MenuFrame.GetHorizontalFrameSize()-2)
		return m.width < 36 || available < 9+len(wrapPermissionDetail(fullAccessConfirmation, inner))
	}
	width := min(100, max(1, m.width-m.editor.ContentOffset()))
	inner := width - m.styles.Editor.MenuFrame.GetHorizontalFrameSize() - 2
	leftWidth := permissionLabelWidth()
	detailWidth := inner - leftWidth - 2
	if detailWidth < 12 {
		return true
	}
	height := 6 + len(wrapPermissionDetail(permissionOptionDetails[0], detailWidth)) + len(wrapPermissionDetail(permissionOptionDetails[1], detailWidth))
	return available < height
}

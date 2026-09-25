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

func (m *Model) permissionsBusy() bool {
	return m.turnEvents != nil || m.cancelTurn != nil || m.chatPhase == chat.PhaseLoading || len(m.pendingApprovals) > 0
}

func (m *Model) switchPermissions(argument string) tea.Cmd {
	if m.tools == nil || m.engine == nil {
		return m.showNotice(notice(chat.NoticeError, nil, "This session has no tool permissions to configure."), 0)
	}
	current := m.tools.PermissionsMode()
	if argument == "" {
		if m.permissionsBusy() {
			return m.showNotice(notice(chat.NoticeInfo, nil, "Permissions: %s (this session). Changes require an idle session.", current), 0)
		}
		m.permissionChoice = 0
		if current == agent.ModeSkipPermissions {
			m.permissionChoice = 1
		}
		m.permissionConfirm = false
		m.permissionAllow = false
		m.editor.CloseMenu()
		m.setMode(ModePermissions)
		return nil
	}
	if argument != string(agent.ModeManual) && argument != string(agent.ModeSkipPermissions) {
		return m.showNotice(notice(chat.NoticeError, nil, "Unknown permissions mode %q. Use manual or skip-permissions.", argument), 0)
	}
	if m.permissionsBusy() {
		return m.showNotice(notice(chat.NoticeWarn, nil, "Wait for the assistant response and any permission request to finish before switching permissions."), 0)
	}
	mode := agent.PermissionsMode(argument)
	if mode == current {
		return m.showNotice(notice(chat.NoticeInfo, nil, "Permissions: already %s.", mode), 0)
	}
	if current == agent.ModeManual && mode == agent.ModeSkipPermissions {
		m.permissionChoice = 1
		m.permissionConfirm = true
		m.permissionAllow = false
		m.editor.CloseMenu()
		m.setMode(ModePermissions)
		return nil
	}
	return m.applyPermissionsMode(mode)
}

func (m *Model) applyPermissionsMode(mode agent.PermissionsMode) tea.Cmd {
	if m.permissionsBusy() {
		m.setMode(ModeChat)
		return m.showNotice(notice(chat.NoticeWarn, nil, "Permissions can change only in an idle session."), 0)
	}
	if err := m.engine.SetPermissionsMode(m.tools, mode); err != nil {
		m.setMode(ModeChat)
		if errors.Is(err, agent.ErrOperationActive) {
			return m.showNotice(notice(chat.NoticeWarn, nil, "Permissions can change only in an idle session."), 0)
		}
		return m.showNotice(notice(chat.NoticeError, err, "Could not switch the permissions mode."), 0)
	}
	m.setMode(ModeChat)
	m.syncStatus()
	if mode == agent.ModeSkipPermissions {
		return m.showNotice(notice(chat.NoticeWarn, nil, "Permissions: skip-permissions. Tools will run without asking."), 0)
	}
	return m.showNotice(notice(chat.NoticeInfo, nil, "Permissions: manual. Tools will ask before running."), 0)
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
			m.setMode(ModeChat)
			return nil
		}
		if mode == agent.ModeSkipPermissions {
			m.permissionConfirm = true
			m.permissionAllow = false
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
	header := "Permissions"
	if m.permissionConfirm {
		header = "Full access?"
	}
	closeHint := "ESC x"
	headerGap := strings.Repeat(" ", max(1, inner-ansi.StringWidth(header)-ansi.StringWidth(closeHint)))
	rows := []string{panel.Title.Render(header) + headerGap + panel.Dismiss.Render(closeHint)}
	if m.permissionsCompact() {
		rows = append(rows, line("Resize terminal to choose permissions", false))
	} else if m.permissionConfirm {
		rows = append(rows,
			line("", false),
			line("Tools will run without approval prompts.", false),
			line("This includes local and server gated actions.", false),
			line("", false),
			line(permissionMarker(m.permissionAllow)+"Yes, enable full access", m.permissionAllow),
			line(permissionMarker(!m.permissionAllow)+"Cancel", !m.permissionAllow),
		)
	} else {
		labels := m.permissionOptionLabels()
		leftWidth := max(ansi.StringWidth(labels[0]), ansi.StringWidth(labels[1])) + 2
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
	rows = append(rows, line("", false))
	return style.MenuFrame.Width(width).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func (m *Model) permissionOptionLabels() [2]string {
	labels := [2]string{"Ask for Approval", "Full access"}
	if m.tools.PermissionsMode() == agent.ModeManual {
		labels[0] += " (current)"
	} else {
		labels[1] += " (current)"
	}
	return labels
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
	if m.permissionConfirm {
		return m.width < 36 || available < 10
	}
	width := min(100, max(1, m.width-m.editor.ContentOffset()))
	inner := width - m.styles.Editor.MenuFrame.GetHorizontalFrameSize() - 2
	labels := m.permissionOptionLabels()
	leftWidth := max(ansi.StringWidth(labels[0]), ansi.StringWidth(labels[1])) + 2
	detailWidth := inner - leftWidth - 2
	if detailWidth < 12 {
		return true
	}
	height := 6 + len(wrapPermissionDetail(permissionOptionDetails[0], detailWidth)) + len(wrapPermissionDetail(permissionOptionDetails[1], detailWidth))
	return available < height
}

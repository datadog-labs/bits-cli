package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/components"
)

var permissionModes = [...]agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions}

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
	width := min(60, max(1, m.width-m.editor.ContentOffset()))
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
		header = "Skip permissions?"
	}
	closeHint := "ESC x"
	headerGap := strings.Repeat(" ", max(1, inner-ansi.StringWidth(header)-ansi.StringWidth(closeHint)))
	rows := []string{panel.Title.Render(header) + headerGap + panel.Dismiss.Render(closeHint)}
	if m.permissionsCompact() {
		rows = append(rows, line("Resize terminal to choose permissions", false))
	} else if m.permissionConfirm {
		rows = append(rows,
			line("Tools will run without approval prompts.", false),
			line("This includes local and server gated actions.", false),
			line("", false),
			line(permissionMarker(!m.permissionAllow)+"Cancel", !m.permissionAllow),
			line(permissionMarker(m.permissionAllow)+"Switch to skip-permissions", m.permissionAllow),
			line("Enter to select", false),
		)
	} else {
		manualLabel := "Manual"
		skipLabel := "Skip permissions"
		if m.tools.PermissionsMode() == agent.ModeManual {
			manualLabel += "  Current"
		} else {
			skipLabel += "  Current"
		}
		selectorStyles := m.styles.Selector
		selectorStyles.Item = m.styles.Text.Secondary
		selectorStyles.Detail = m.styles.Text.Tertiary
		selectorStyles.SelectedDetail = selectorStyles.Selected
		selector := components.NewSelector([]components.Choice{
			{Label: manualLabel, Detail: "Ask before gated tools"},
			{Label: skipLabel, Detail: "No approval prompts"},
		}, selectorStyles)
		selector.SetAlignDetailRight(true)
		selector.SetIndex(m.permissionChoice)
		optionRows := strings.Split(selector.View(inner), "\n")
		rows = append(rows,
			line("", false),
			optionRows[0],
			line("", false),
			optionRows[1],
			line("", false),
			m.styles.Panel.Help.Render("↑/↓ to choose · Enter to select"),
		)
	}
	return style.MenuFrame.BorderForeground(m.styles.Selector.Selected.GetForeground()).Width(width).Padding(0, 1).Render(strings.Join(rows, "\n"))
}

func permissionMarker(selected bool) string {
	if selected {
		return "› "
	}
	return "  "
}

func (m *Model) permissionsCompact() bool {
	return m.width < 36 || m.height-chatFooterHeight-m.editor.Height() < 9
}

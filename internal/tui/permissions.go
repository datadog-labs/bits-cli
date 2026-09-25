package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
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
	if m.width < 36 || m.height < 8 {
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
	if m.width < 36 || m.height < 8 {
		return lipgloss.Place(max(1, m.width), max(1, m.height), lipgloss.Center, lipgloss.Center,
			ansi.Truncate("Resize terminal to choose permissions", max(1, m.width), ""))
	}
	var rows []string
	if m.permissionConfirm {
		rows = []string{
			"Switch to skip-permissions?", "", "Tools will run without approval prompts, including local and server gated actions.", "", "  Cancel", "  Switch to skip-permissions", "", "Enter to select · Esc to cancel",
		}
		if m.permissionAllow {
			rows[5] = "› Switch to skip-permissions"
		} else {
			rows[4] = "› Cancel"
		}
	} else {
		rows = []string{"Permissions", "", "  Manual · Ask for approval when a tool requires it.", "  Skip permissions · Run tools without asking for approval.", "", "↑/↓ to choose · Enter to select · Esc to cancel"}
		rows[2+m.permissionChoice] = "›" + rows[2+m.permissionChoice][1:]
	}
	width := max(1, m.width)
	for i, row := range rows {
		rows[i] = ansi.Truncate(row, width, "")
	}
	content := strings.Join(rows, "\n")
	return lipgloss.Place(width, max(1, m.height), lipgloss.Center, lipgloss.Center, content)
}

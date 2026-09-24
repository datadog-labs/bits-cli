package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

func (m *Model) switchPermissions(argument string) tea.Cmd {
	if m.tools == nil {
		return m.showNotice(notice(chat.NoticeError, nil, "This session has no tool permissions to configure."), 0)
	}
	current := m.tools.PermissionsMode()
	switch argument {
	case "":
		return m.showNotice(notice(chat.NoticeInfo, nil, "Permissions: %s (this session).", current), 0)
	case string(agent.ModeManual), string(agent.ModeSkipPermissions):
		mode := agent.PermissionsMode(argument)
		if mode == current {
			return m.showNotice(notice(chat.NoticeInfo, nil, "Permissions: already %s.", mode), 0)
		}
		if err := m.tools.SetPermissionsMode(mode); err != nil {
			return m.showNotice(notice(chat.NoticeError, err, "Could not switch the permissions mode."), 0)
		}
		m.syncStatus()
		if mode == agent.ModeSkipPermissions {
			return m.showNotice(notice(chat.NoticeWarn, nil, "Permissions: skip-permissions. Tools will run without asking."), 0)
		}
		return m.showNotice(notice(chat.NoticeInfo, nil, "Permissions: manual. Tools will ask before running."), 0)
	default:
		return m.showNotice(notice(chat.NoticeError, nil, "Unknown permissions mode %q. Use manual or skip-permissions.", argument), 0)
	}
}

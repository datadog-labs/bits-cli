package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
)

// permissionModes are the modes permissionOptions offer, in their order.
var permissionModes = [...]agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions}

var permissionOptions = [...]components.Option{
	{Label: "Ask for Approval", Description: "Bits will ask for approval before making any changes in your workspace"},
	{Label: "Full Access", Description: "Use with caution: Bits will execute actions on your behalf"},
}

const fullAccessConfirmation = "Enabling full access will automatically approve all actions without requiring confirmation."

var fullAccessOptions = [...]components.Option{{Label: "Yes, enable full access"}, {Label: "Cancel"}}

func (m *Model) switchPermissions(argument string) tea.Cmd {
	if m.tools == nil || m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "This session has no tool permissions to configure."))
	}
	current := m.engine.PermissionsMode()
	if argument == "" {
		m.openPermissionsPrompt(current)
		return nil
	}
	if argument != string(agent.ModeManual) && argument != string(agent.ModeSkipPermissions) {
		return m.postNotice(notice(chat.NoticeError, nil, "Unknown permissions mode %q. Use manual or skip-permissions.", argument))
	}
	return m.choosePermissionsMode(agent.PermissionsMode(argument))
}

// openPermissionsPrompt offers the modes with the current one marked and
// highlighted.
func (m *Model) openPermissionsPrompt(current agent.PermissionsMode) {
	index := slices.Index(permissionModes[:], current)
	form := components.NewChoicePrompt("Manage Bits Permissions", "Permission changes take effect immediately.", permissionOptions[:], index)
	form.SetCurrent(index)
	openCommandPrompt(m, form, func(choice int) tea.Cmd { return m.choosePermissionsMode(permissionModes[choice]) })
}

// choosePermissionsMode switches to mode, after a confirmation when it grants
// full access.
func (m *Model) choosePermissionsMode(mode agent.PermissionsMode) tea.Cmd {
	if mode == m.engine.PermissionsMode() {
		return nil
	}
	if mode == agent.ModeSkipPermissions {
		m.confirmFullAccess()
		return nil
	}
	return m.setPermissionsMode(mode)
}

func (m *Model) confirmFullAccess() {
	form := components.NewChoicePrompt("Full Access", fullAccessConfirmation, fullAccessOptions[:], 0)
	openCommandPrompt(m, form, func(choice int) tea.Cmd {
		if choice != 0 {
			return nil
		}
		if m.engine.PermissionsMode() != agent.ModeManual {
			return m.postNotice(notice(chat.NoticeWarn, nil, "Permissions changed while the confirmation was open. Run /permissions again."))
		}
		return m.setPermissionsMode(agent.ModeSkipPermissions)
	})
}

// setPermissionsMode switches the mode at once, mid-turn included: the engine
// re-gates calls already waiting for approval.
func (m *Model) setPermissionsMode(mode agent.PermissionsMode) tea.Cmd {
	if err := m.engine.SetPermissionsMode(mode); err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not switch the permissions mode."))
	}
	m.syncStatus()
	return m.postNotice(permissionsChangedNotice(mode))
}

func permissionsChangedNotice(mode agent.PermissionsMode) chat.Notice {
	if mode == agent.ModeSkipPermissions {
		return notice(chat.NoticeWarn, nil, "Full Access enabled. Actions will run without approval.")
	}
	return notice(chat.NoticeInfo, nil, "Permissions set to Ask for Approval.")
}

package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/components"
)

var permissionModes = [...]agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions}

var permissionOptionDetails = [...]string{
	"Bits will ask for approval before making any changes in your workspace",
	"Use with caution: Bits will execute actions on your behalf",
}

const fullAccessConfirmation = "Enabling full access will automatically approve all actions without requiring confirmation."

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
	if !m.permissionsPanel.Fits(max(1, m.width), m.frame.composerTop(), m.permissionsContent()) {
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

// permissionsView floats above the composer, so it fits the rows left there
// and falls back to the panel's compact form when it does not.
func (m *Model) permissionsView() string {
	return m.permissionsPanel.Render(max(1, m.width), m.frame.composerTop(), m.permissionsContent())
}

func (m *Model) permissionsContent() components.PanelContent {
	content := components.PanelContent{
		Title:   "Manage Bits Permissions",
		Dismiss: "ESC x",
		Body:    m.permissionsBody,
		// Narrower than this, the option details wrap a word per row.
		MinBodyWidth:   permissionLabelWidth + 2 + 12,
		CompactMessage: "Resize terminal to choose permissions",
	}
	if m.permissionConfirm {
		// The body inside a 36-column terminal.
		content.Title, content.MinBodyWidth = "Full Access", 26
	}
	return content
}

// permissionLabelWidth keeps the detail column fixed when the current suffix
// moves between options, so wrapping and vertical spacing do not jump.
var permissionLabelWidth = ansi.StringWidth("› Ask for Approval (current)") + 2

// permissionsBody renders the rows below the title; the panel supplies the
// gap above them. Every row fills the width so the popup stays opaque.
func (m *Model) permissionsBody(width, _ int) string {
	style := m.styles.Editor
	row := func(rowStyle lipgloss.Style, value string) string {
		return rowStyle.Width(width).Render(ansi.Truncate(value, width, "…"))
	}
	choice := func(label string, selected bool) string {
		if selected {
			return row(style.MenuSelected, "› "+label)
		}
		return row(style.MenuItem, "  "+label)
	}
	blank := row(style.MenuItem, "")

	var rows []string
	if m.permissionConfirm {
		for _, line := range wordwrap(fullAccessConfirmation, width) {
			rows = append(rows, row(style.MenuDetail, line))
		}
		rows = append(rows, blank, choice("Yes, enable full access", m.permissionAllow), blank, choice("Cancel", !m.permissionAllow))
		return strings.Join(rows, "\n")
	}
	rows = append(rows, row(style.MenuDetail, "Permission changes take effect on the next turn."))
	for i, label := range m.permissionOptionLabels() {
		rows = append(rows, blank)
		rows = append(rows, m.permissionOptionRows(label, permissionOptionDetails[i], width, i == m.permissionChoice)...)
	}
	return strings.Join(rows, "\n")
}

func (m *Model) permissionOptionLabels() [2]string {
	labels := [2]string{"Ask for Approval", "Full Access"}
	selected := m.tools.PermissionsMode()
	if m.pendingPermissions != "" {
		selected = m.pendingPermissions
	}
	if selected == agent.ModeManual {
		labels[0] += " (current)"
	} else {
		labels[1] += " (current)"
	}
	return labels
}

// permissionOptionRows puts the label in a fixed left column and wraps the
// detail beside it.
func (m *Model) permissionOptionRows(label, detail string, width int, selected bool) []string {
	labelStyle, detailStyle, marker := m.styles.Text.Secondary, m.styles.Text.Tertiary, "  "
	if selected {
		labelStyle, detailStyle, marker = m.styles.Selector.Selected, m.styles.Selector.Selected, "› "
	}
	left := labelStyle.Width(permissionLabelWidth)
	var rows []string
	for i, part := range wordwrap(detail, width-permissionLabelWidth-2) {
		if i == 0 {
			part = left.Render(marker+label) + "  " + detailStyle.Render(part)
		} else {
			part = strings.Repeat(" ", permissionLabelWidth+2) + detailStyle.Render(part)
		}
		rows = append(rows, part+strings.Repeat(" ", max(0, width-ansi.StringWidth(part))))
	}
	return rows
}

func wordwrap(value string, width int) []string {
	return strings.Split(ansi.Wordwrap(value, max(1, width), ""), "\n")
}

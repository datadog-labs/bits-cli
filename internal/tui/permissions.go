package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
)

var permissionModes = [...]agent.PermissionsMode{agent.ModeManual, agent.ModeSkipPermissions}

var permissionOptionDetails = [...]string{
	"Bits will ask for approval before making any changes in your workspace",
	"Use with caution: Bits will execute actions on your behalf",
}

const fullAccessConfirmation = "Enabling full access will automatically approve all actions without requiring confirmation."

func (m *Model) switchPermissions(argument string) tea.Cmd {
	if m.tools == nil || m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "This session has no tool permissions to configure."))
	}
	current := m.engine.PermissionsMode()
	if argument == "" {
		m.permissionConfirm, m.permissionCursor = false, slices.Index(permissionModes[:], current)
		m.editor.CloseMenu()
		m.setMode(ModePermissions)
		return nil
	}
	if argument != string(agent.ModeManual) && argument != string(agent.ModeSkipPermissions) {
		return m.postNotice(notice(chat.NoticeError, nil, "Unknown permissions mode %q. Use manual or skip-permissions.", argument))
	}
	mode := agent.PermissionsMode(argument)
	if mode == current {
		return nil
	}
	if current == agent.ModeManual && mode == agent.ModeSkipPermissions {
		m.permissionConfirm, m.permissionCursor = true, 0
		m.editor.CloseMenu()
		m.setMode(ModePermissions)
		return nil
	}
	return m.applyPermissionsMode(mode)
}

// applyPermissionsMode closes the picker and switches the mode.
func (m *Model) applyPermissionsMode(mode agent.PermissionsMode) tea.Cmd {
	m.setMode(ModeChat)
	return m.setPermissionsMode(mode)
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

func (m *Model) updatePermissionsKey(msg tea.KeyPressMsg) tea.Cmd {
	if !m.permissionsPanel.Fits(max(1, m.width), m.frame.inputTop(), m.permissionsContent()) {
		if msg.String() == "esc" {
			m.setMode(ModeChat)
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		m.setMode(ModeChat)
	case "up", "down", "left", "right", "tab", "shift+tab":
		// Both pages have two rows.
		m.permissionCursor = 1 - m.permissionCursor
	case "enter":
		if m.permissionConfirm {
			if m.permissionCursor != 0 {
				m.setMode(ModeChat)
				return nil
			}
			if m.engine.PermissionsMode() != agent.ModeManual {
				m.setMode(ModeChat)
				return m.postNotice(notice(chat.NoticeWarn, nil, "Permissions changed while the picker was open. Open it again."))
			}
			return m.applyPermissionsMode(agent.ModeSkipPermissions)
		}
		mode := permissionModes[m.permissionCursor]
		if mode == m.engine.PermissionsMode() {
			m.setMode(ModeChat)
			return nil
		}
		if mode == agent.ModeSkipPermissions {
			m.permissionConfirm, m.permissionCursor = true, 0
			return nil
		}
		return m.applyPermissionsMode(mode)
	}
	return nil
}

// permissionsView floats above the composer, so it fits the rows left there
// and falls back to the panel's compact form when it does not.
func (m *Model) permissionsView() string {
	return m.permissionsPanel.Render(max(1, m.width), m.frame.inputTop(), m.permissionsContent())
}

func (m *Model) permissionsContent() components.PanelContent {
	content := components.PanelContent{
		Title:   "Manage Bits Permissions",
		Dismiss: "ESC x",
		Body:    m.permissionsBody,
		// Narrower than this, the option details wrap a word per row.
		MinBodyWidth:   permissionLabelWidth + 2 + 12,
		CompactMessage: "Resize terminal to choose permissions",
		TinyMessage:    "Resize to choose permissions",
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
		for _, line := range textwrap.WrapProse(width, fullAccessConfirmation, nil) {
			rows = append(rows, row(style.MenuDetail, line))
		}
		rows = append(rows, blank, choice("Yes, enable full access", m.permissionCursor == 0), blank, choice("Cancel", m.permissionCursor == 1))
		return strings.Join(rows, "\n")
	}
	rows = append(rows, row(style.MenuDetail, "Permission changes take effect immediately."))
	for i, label := range m.permissionOptionLabels() {
		rows = append(rows, blank)
		rows = append(rows, m.permissionOptionRows(label, permissionOptionDetails[i], width, i == m.permissionCursor)...)
	}
	return strings.Join(rows, "\n")
}

func (m *Model) permissionOptionLabels() [2]string {
	labels := [2]string{"Ask for Approval", "Full Access"}
	if m.engine.PermissionsMode() == agent.ModeManual {
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
	rows := textwrap.Wrap(textwrap.Options{
		Width:       width,
		FirstPrefix: labelStyle.Width(permissionLabelWidth).Render(marker+label) + "  ",
		RestPrefix:  strings.Repeat(" ", permissionLabelWidth+2),
		Breakpoints: textwrap.Prose,
		TrimSpace:   true,
	}, textwrap.Styled(detail, detailStyle))
	for i, row := range rows {
		rows[i] = row + strings.Repeat(" ", max(0, width-ansi.StringWidth(row)))
	}
	return rows
}

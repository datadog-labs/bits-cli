package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// activeTurnPolicy declares how a command behaves while a turn or history
// restore is active. Keeping this policy beside command metadata lets submit
// route commands before applying the ordinary-message busy guard.
type activeTurnPolicy uint8

const (
	commandAllowedDuringTurn activeTurnPolicy = iota + 1
	commandRejectedDuringTurn
	commandCancelsTurn
)

type commandID uint8

const (
	commandQuit commandID = iota + 1
	commandNew
	commandResume
	commandStatus
	commandCopy
	commandWeb
	commandSettings
	commandLogout
	commandPermissions
)

type commandDefinition struct {
	id               commandID
	name             string
	aliases          []string
	activeTurnPolicy activeTurnPolicy
}

var commandDefinitions = []commandDefinition{
	{
		id:               commandNew,
		name:             "new",
		aliases:          []string{"clear"},
		activeTurnPolicy: commandCancelsTurn,
	},
	{
		id:               commandQuit,
		name:             "quit",
		aliases:          []string{"exit"},
		activeTurnPolicy: commandCancelsTurn,
	},
	{
		id:               commandResume,
		name:             "resume",
		activeTurnPolicy: commandRejectedDuringTurn,
	},
	{
		id:               commandStatus,
		name:             "status",
		activeTurnPolicy: commandAllowedDuringTurn,
	},
	{
		id:               commandCopy,
		name:             "copy",
		activeTurnPolicy: commandRejectedDuringTurn,
	},
	{
		id:               commandWeb,
		name:             "web",
		activeTurnPolicy: commandAllowedDuringTurn,
	},
	{
		id:               commandSettings,
		name:             "settings",
		activeTurnPolicy: commandAllowedDuringTurn,
	},
	{
		id:               commandLogout,
		name:             "logout",
		activeTurnPolicy: commandCancelsTurn,
	},
	{
		id:               commandPermissions,
		name:             "permissions",
		activeTurnPolicy: commandRejectedDuringTurn,
	},
}

func lookupCommand(name string) (commandDefinition, bool) {
	for _, definition := range commandDefinitions {
		if name == definition.name {
			return definition, true
		}
		for _, alias := range definition.aliases {
			if name == alias {
				return definition, true
			}
		}
	}
	return commandDefinition{}, false
}

// parseCommand recognizes a submitted slash command. It returns the command
// name (lowercased, no leading "/") and its lowercased argument remainder
// when the input is a single leading "/token" at the beginning of the
// prompt. A bare "/", a "/ word" form, or leading whitespace before the
// slash is not a command, so it falls through to a normal agent turn.
func parseCommand(input string) (string, string, bool) {
	if !strings.HasPrefix(input, "/") {
		return "", "", false
	}
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return "", "", false
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if name == "" {
		return "", "", false
	}
	argument := ""
	if len(fields) > 1 {
		argument = strings.ToLower(strings.Join(fields[1:], " "))
	}
	return name, argument, true
}

// dispatchCommand routes a parsed slash command and its argument remainder to
// its handler and returns the (model, cmd) the caller returns from Update.
// Recognized commands own their side effects (turn cancellation, notices).
// Unrecognized commands post a transient notice rather than reaching the
// model, so the control plane never leaks literal slash text into an agent
// turn.
func (m *Model) dispatchCommand(name, argument string) (tea.Model, tea.Cmd) {
	definition, ok := lookupCommand(name)
	if !ok {
		return m, m.showNotice(notice(chat.NoticeError, nil, "Unknown command: /%s", name), 0)
	}
	if m.pendingLogout || m.logoutRunning {
		return m, m.showNotice(notice(chat.NoticeInfo, nil, "Logout is already in progress."), 0)
	}

	active := m.turnEvents != nil || m.cancelTurn != nil || m.chatPhase == chat.PhaseLoading || len(m.pendingApprovals) > 0
	if active {
		switch definition.activeTurnPolicy {
		case commandRejectedDuringTurn:
			if definition.id == commandCopy {
				return m, m.showNotice(notice(chat.NoticeWarn, nil, "Wait for the assistant response to finish before using /copy."), 0)
			}
			if definition.id == commandPermissions {
				return m, m.showNotice(notice(chat.NoticeWarn, nil, "Wait for the assistant response and any permission request to finish before switching permissions."), 0)
			}
			return m, m.showNotice(notice(chat.NoticeWarn, nil, "Command unavailable during an active turn: /%s", name), 0)
		case commandCancelsTurn:
			switch definition.id {
			case commandNew:
				return m, m.requestNewConversation()
			case commandLogout:
				return m, m.requestLogout()
			case commandQuit, commandResume, commandStatus, commandWeb, commandSettings:
				if m.cancelTurn != nil {
					m.cancelTurn()
				}
			default:
				panic("unhandled command cancellation policy")
			}
		case commandAllowedDuringTurn:
			// Continue to the handler without disturbing the active turn.
		default:
			panic("invalid active-turn command policy")
		}
	}

	switch definition.id {
	case commandNew:
		return m, m.startNewConversation()
	case commandQuit:
		return m.quit()
	case commandResume:
		return m, batchCommands(m.stopCompletionSearches(), m.openConversationPicker())
	case commandStatus:
		return m, batchCommands(m.stopCompletionSearches(), m.openStatus())
	case commandCopy:
		return m, m.copyLatestAssistantResponse()
	case commandWeb:
		return m, batchCommands(m.stopCompletionSearches(), m.openConversationInBrowser())
	case commandSettings:
		return m, batchCommands(m.stopCompletionSearches(), m.openSettingsInBrowser())
	case commandLogout:
		return m, m.startLogout()
	case commandPermissions:
		return m, m.switchPermissions(argument)
	default:
		panic("unhandled registered command")
	}
}

// copyLatestAssistantResponse copies the most recent completed assistant text
// block. Transcript source, rather than terminal rendering, keeps Markdown
// predictable and excludes styles, hidden reasoning, tools, and metadata. The
// terminal owns the OSC 52 clipboard write, matching text selection behavior.
func (m *Model) copyLatestAssistantResponse() tea.Cmd {
	text, ok := latestCopyableAssistantResponse(m.blocks)
	if !ok {
		return m.showNotice(notice(chat.NoticeWarn, nil, "No completed assistant response is available to copy."), 0)
	}
	return tea.Batch(
		m.showNotice(notice(chat.NoticeInfo, nil, "Copied to clipboard."), 0),
		tea.SetClipboard(text),
	)
}

func latestCopyableAssistantResponse(blocks []agent.Block) (string, bool) {
	for i := len(blocks) - 1; i >= 0; i-- {
		block := blocks[i]
		if block.Role != assistant.RoleAssistant || block.Kind != assistant.KindText || !block.Complete || block.Markdown == nil {
			continue
		}
		text := ansi.Strip(block.Markdown.Content)
		if text != "" {
			return text, true
		}
	}
	return "", false
}

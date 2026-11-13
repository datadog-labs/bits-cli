package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
)

// activeOperationPolicy declares how a command behaves while an exclusive
// session operation is active. Keeping this beside command metadata lets submit
// route commands before applying the ordinary-message busy guard.
type activeOperationPolicy uint8

const (
	commandAllowedDuringOperation activeOperationPolicy = iota + 1
	commandRejectedDuringOperation
	commandCancelsOperation
)

type commandID uint8

const (
	commandQuit commandID = iota + 1
	commandNew
	commandResume
	commandFork
	commandRename
	commandStatus
	commandCopy
	commandExport
	commandWeb
	commandSettings
	commandLogout
	commandPermissions
	commandUpdate
)

type commandDefinition struct {
	id                    commandID
	name                  string
	aliases               []string
	activeOperationPolicy activeOperationPolicy
	description           string
}

var commandDefinitions = []commandDefinition{
	{id: commandFork, name: "fork", aliases: []string{"branch"}, activeOperationPolicy: commandRejectedDuringOperation, description: "fork this conversation and continue in the copy"},
	{id: commandRename, name: "rename", activeOperationPolicy: commandRejectedDuringOperation, description: "rename this conversation"},
	{
		id:                    commandNew,
		name:                  "new",
		aliases:               []string{"clear"},
		activeOperationPolicy: commandCancelsOperation,
		description:           "start a new conversation",
	},
	{
		id:                    commandQuit,
		name:                  "quit",
		aliases:               []string{"exit"},
		activeOperationPolicy: commandCancelsOperation,
		description:           "exit bits",
	},
	{
		id:                    commandResume,
		name:                  "resume",
		activeOperationPolicy: commandRejectedDuringOperation,
		description:           "resume a conversation",
	},
	{
		id:                    commandStatus,
		name:                  "status",
		activeOperationPolicy: commandAllowedDuringOperation,
		description:           "show session status",
	},
	{
		id:                    commandCopy,
		name:                  "copy",
		activeOperationPolicy: commandRejectedDuringOperation,
		description:           "copy the latest assistant response",
	},
	{
		id:                    commandExport,
		name:                  "export",
		activeOperationPolicy: commandRejectedDuringOperation,
		description:           "copy the whole conversation as markdown",
	},
	{
		id:                    commandWeb,
		name:                  "web",
		activeOperationPolicy: commandAllowedDuringOperation,
		description:           "open this conversation in Datadog",
	},
	{
		id:                    commandSettings,
		name:                  "settings",
		activeOperationPolicy: commandAllowedDuringOperation,
		description:           "open assistant settings",
	},
	{
		id:                    commandLogout,
		name:                  "logout",
		activeOperationPolicy: commandCancelsOperation,
		description:           "sign out from your Datadog account",
	},
	{
		id:                    commandPermissions,
		name:                  "permissions",
		activeOperationPolicy: commandAllowedDuringOperation,
		description:           "choose what Bits is allowed to do",
	},
	{
		id:                    commandUpdate,
		name:                  "update",
		aliases:               []string{"upgrade"},
		activeOperationPolicy: commandCancelsOperation,
		description:           "exit and update bits",
	},
}

func commandCompletionSpecs() []editor.CommandSpec {
	specs := make([]editor.CommandSpec, 0, len(commandDefinitions))
	for _, definition := range commandDefinitions {
		specs = append(specs, editor.CommandSpec{Name: definition.name, Aliases: definition.aliases, Detail: definition.description})
	}
	return specs
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
// name (lowercased, no leading "/") and its argument remainder, with case kept
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
		argument = strings.Join(fields[1:], " ")
	}
	return name, argument, true
}

// dispatchCommand routes a parsed slash command and its argument remainder to
// its handler and returns the (model, cmd) the caller returns from Update.
// Recognized commands own their side effects (turn cancellation, notices).
// Unrecognized commands post a local transcript message rather than reaching the
// model, so the control plane never leaks literal slash text into an agent
// turn.
func (m *Model) dispatchCommand(name, argument string) (tea.Model, tea.Cmd) {
	// A command is a direct user action. If it posts a message while the user is
	// reading above the tail, bring that response into view. Asynchronous notices
	// use postNotice directly and preserve the reader's scroll position.
	before := m.nextNoticeID
	defer func() {
		if m.nextNoticeID != before && m.list != nil {
			m.list.ScrollToBottom()
		}
	}()

	definition, ok := lookupCommand(name)
	if !ok {
		return m, m.postNotice(notice(chat.NoticeError, nil, "Unknown command: /%s", name))
	}
	if m.op.loggingOut() {
		return m, m.postNotice(notice(chat.NoticeInfo, nil, "Logout is already in progress."))
	}

	if m.op.busy() {
		switch definition.activeOperationPolicy {
		case commandRejectedDuringOperation:
			if definition.id == commandCopy || definition.id == commandExport {
				return m, m.postNotice(notice(chat.NoticeWarn, nil, "Wait for the current operation to finish before using /%s.", definition.name))
			}
			return m, m.postNotice(notice(chat.NoticeWarn, nil, "Command unavailable during an active operation: /%s", name))
		case commandCancelsOperation:
			switch definition.id {
			case commandNew:
				return m, m.requestNewConversation()
			case commandLogout:
				return m, m.requestLogout()
			case commandQuit, commandUpdate:
				// quit cancels the running operation itself.
			default:
				panic("unhandled command cancellation policy")
			}
		case commandAllowedDuringOperation:
			// Continue without disturbing the active operation.
		default:
			panic("invalid active-operation command policy")
		}
	}

	switch definition.id {
	case commandNew:
		return m, m.startNewConversation()
	case commandQuit:
		return m.quit()
	case commandFork:
		return m, m.startFork(argument)
	case commandRename:
		return m, m.startRename(argument)
	case commandResume:
		return m, tea.Batch(m.stopCompletionSearches(), m.openConversationPicker())
	case commandStatus:
		return m, tea.Batch(m.stopCompletionSearches(), m.openStatus())
	case commandCopy:
		return m, m.copyLatestAssistantResponse()
	case commandExport:
		return m, m.exportConversation()
	case commandWeb:
		return m, tea.Batch(m.stopCompletionSearches(), m.openConversationInBrowser())
	case commandSettings:
		return m, tea.Batch(m.stopCompletionSearches(), m.openSettingsInBrowser())
	case commandLogout:
		return m, m.startLogout()
	case commandPermissions:
		return m, tea.Batch(m.stopCompletionSearches(), m.switchPermissions(strings.ToLower(argument)))
	case commandUpdate:
		return m.requestUpgrade()
	default:
		panic("unhandled registered command")
	}
}

// copyLatestAssistantResponse copies the most recent completed assistant text
// block. Transcript source, rather than terminal rendering, keeps Markdown
// predictable and excludes styles, hidden reasoning, tools, and metadata. The
// terminal owns the OSC 52 clipboard write, matching text selection behavior.
func (m *Model) copyLatestAssistantResponse() tea.Cmd {
	text, ok := latestCopyableAssistantResponse(m.state.Blocks)
	if !ok {
		return m.postNotice(notice(chat.NoticeWarn, nil, "No completed assistant response is available to copy."))
	}
	return tea.Batch(
		m.postNotice(notice(chat.NoticeInfo, nil, "Copied to clipboard.")),
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

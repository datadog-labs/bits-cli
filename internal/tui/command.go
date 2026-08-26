package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

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
// name (lowercased, no leading "/") and true when the input is a single leading
// "/token"; anything after whitespace is treated as arguments and ignored. A
// bare "/" or a "/ word" form is not a command, so it falls through to a normal
// agent turn. The caller passes already-trimmed text.
func parseCommand(input string) (string, bool) {
	if !strings.HasPrefix(input, "/") {
		return "", false
	}
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return "", false
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	if name == "" {
		return "", false
	}
	return name, true
}

// dispatchCommand routes a parsed slash command to its handler and returns the
// (model, cmd) the caller returns from Update. Recognized commands own their
// side effects (turn cancellation, notices). Unrecognized commands post a
// transient notice rather than reaching the model, so the control plane never
// leaks literal slash text into an agent turn.
func (m *Model) dispatchCommand(name string) (tea.Model, tea.Cmd) {
	definition, ok := lookupCommand(name)
	if !ok {
		return m, m.showNotice(notice(chat.NoticeError, nil, "Unknown command: /%s", name), 0)
	}

	active := m.turnEvents != nil || m.cancelTurn != nil || m.chatPhase == chat.PhaseLoading
	if active {
		switch definition.activeTurnPolicy {
		case commandRejectedDuringTurn:
			return m, m.showNotice(notice(chat.NoticeWarn, nil, "Command unavailable during an active turn: /%s", name), 0)
		case commandCancelsTurn:
			if definition.id == commandNew {
				return m, m.requestNewConversation()
			}
			if m.cancelTurn != nil {
				m.cancelTurn()
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
		return m, tea.Quit
	default:
		panic("unhandled registered command")
	}
}

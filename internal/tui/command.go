package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

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
	switch name {
	case "quit":
		if m.cancelTurn != nil {
			m.cancelTurn()
		}
		return m, tea.Quit
	default:
		return m, m.showNotice(notice(chat.NoticeError, nil, "Unknown command: /%s", name), 0)
	}
}

package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/DataDog/bits-cli/internal/agent"
)

// turnEventMsg carries one engine event into Update; turnClosedMsg signals the
// turn's channel was closed (turn finished or cancelled).
type (
	turnEventMsg  struct{ ev agent.Event }
	turnClosedMsg struct{}
)

// waitEvent reads one event from the turn channel and re-arms after each event
// in Update — the turn-scoped pump. Reading a closed channel yields
// turnClosedMsg.
func waitEvent(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return turnClosedMsg{}
		}
		return turnEventMsg{ev: ev}
	}
}

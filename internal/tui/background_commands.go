package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// Commands is the source of the commands the assistant left running in the
// background: the chat shows how many run and stops them.
type Commands interface {
	Running() int
	Changed() <-chan struct{}
	StopAll() int
}

// backgroundCommands mirrors how many commands run in the background. They
// belong to the conversation that started them, so leaving it stops them.
type backgroundCommands struct {
	source  Commands // nil when commands cannot run
	running int
	// waiting holds the one wait on source's changes: chat restarts after a
	// login must not add another.
	waiting bool
}

type backgroundCommandsChangedMsg struct{}

// wait delivers the next change, unless a wait is already pending.
func (b *backgroundCommands) wait() tea.Cmd {
	if b.source == nil || b.waiting {
		return nil
	}
	b.waiting = true
	changed := b.source.Changed()
	return func() tea.Msg {
		<-changed
		return backgroundCommandsChangedMsg{}
	}
}

// update records the current count and waits for the next change.
func (b *backgroundCommands) update() tea.Cmd {
	b.waiting = false
	b.running = b.source.Running()
	return b.wait()
}

// stopAll asks every background command to stop and returns how many.
func (b *backgroundCommands) stopAll() int {
	if b.source == nil {
		return 0
	}
	return b.source.StopAll()
}

// label is the footer's mention of running commands, empty when none run.
func (b *backgroundCommands) label() string {
	switch b.running {
	case 0:
		return ""
	case 1:
		return "1 command running"
	default:
		return fmt.Sprintf("%d commands running", b.running)
	}
}

// stoppedCommandsSuffix tells that leaving a conversation stopped its
// commands, so the user does not discover a dead server later.
func stoppedCommandsSuffix(stopped int) string {
	switch stopped {
	case 0:
		return ""
	case 1:
		return " Stopped 1 background command."
	default:
		return fmt.Sprintf(" Stopped %d background commands.", stopped)
	}
}

// stopCommandsNotice reports a /stop.
func stopCommandsNotice(stopped int) string {
	switch stopped {
	case 0:
		return "No background commands are running."
	case 1:
		return "Stopping 1 background command."
	default:
		return fmt.Sprintf("Stopping %d background commands.", stopped)
	}
}

package tui

import (
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

const terminalTickInterval = 100 * time.Millisecond

type terminalTickMsg struct{}

// terminalStatus owns the title animation, turn outcome, and notification focus.
type terminalStatus struct {
	outcome     ansi.ProgramState
	frame       int
	tickPending bool
	focused     bool
}

func (m *Model) updateTerminal(msg tea.Msg) {
	switch msg.(type) {
	case tea.FocusMsg:
		m.terminal.focused = true
	case tea.BlurMsg:
		m.terminal.focused = false
	case tea.KeyPressMsg, tea.PasteMsg, tea.MouseClickMsg:
		m.terminal.outcome = ""
	case terminalTickMsg:
		m.terminal.tickPending = false
		m.terminal.frame = (m.terminal.frame + 1) % 20
	}
}

func (m *Model) terminalProgramStatus() ansi.ProgramStatus {
	status := ansi.ProgramStatus{State: ansi.ProgramStateIdle, App: "bits"}
	switch {
	case m.mode == ModeLogin:
		status.State, status.Kind = ansi.ProgramStateBlocked, ansi.ProgramStatusKindAuth
		if m.startupTask.running() {
			status.State, status.Kind = ansi.ProgramStateWorking, ""
		}
	case m.shown != nil && m.shown.waiting() && m.op.stop != stopAll:
		status.State, status.Kind = ansi.ProgramStateBlocked, ansi.ProgramStatusKindQuestion
		if _, ok := m.shown.(*approvalAsk); ok {
			status.Kind = ansi.ProgramStatusKindPermission
		}
	case m.op.busy() || m.conversationTask.running() || m.mode == ModeTermInit:
		status.State = ansi.ProgramStateWorking
	case m.commandPrompt != nil:
		status.State, status.Kind = ansi.ProgramStateBlocked, ansi.ProgramStatusKindQuestion
	case m.terminal.outcome != "":
		status.State = m.terminal.outcome
	}
	return status
}

// terminalView lets Bubble Tea emit status with the frame, preserving ordering
// during fast transitions and restoring it after terminal suspension.
func (m *Model) terminalView(view tea.View) tea.View {
	view.ReportFocus = true
	if m.widgets.closed || m.startupStopping || m.startupErr != nil {
		return view
	}
	status := m.terminalProgramStatus()
	view.ProgramStatus = &status
	view.WindowTitle = "Bits"
	if status.State == ansi.ProgramStateWorking {
		view.WindowTitle = m.styles.Chat.StatusSpinner.Frame(m.terminal.frame*2) + " Bits"
	}
	if m.workspaceDisplayPath != "" {
		view.WindowTitle += " | " + ansi.Truncate(escape.SingleLine(filepath.Base(m.workspaceDisplayPath)), 80, "…")
	}
	if status.State == ansi.ProgramStateWorking {
		// Value is ignored for indeterminate progress. Changing it once a
		// second refreshes Ghostty's progress timeout through the renderer.
		view.ProgressBar = tea.NewProgressBar(tea.ProgressBarIndeterminate, m.terminal.frame/10)
	}
	return view
}

// One clock animates the title and keeps the terminal's progress bar alive,
// including when the transcript is hidden or has not received its first token.
func (m *Model) syncTerminalStatus() tea.Cmd {
	if m.widgets.closed || m.startupStopping || m.startupErr != nil {
		return nil // runChat clears terminal state after the renderer stops
	}
	if m.terminalProgramStatus().State != ansi.ProgramStateWorking {
		m.terminal.frame = 0
		return nil
	}
	if m.terminal.tickPending {
		return nil
	}
	m.terminal.tickPending = true
	return tea.Tick(terminalTickInterval, func(time.Time) tea.Msg { return terminalTickMsg{} })
}

func (m *Model) observeTerminalOutcome(ev agent.Event) tea.Cmd {
	if m.op.kind != opTurn || !ev.Terminal() {
		return nil
	}
	switch {
	case m.op.stop == stopAll || ev.Kind == agent.EventCanceled:
		m.terminal.outcome = ""
	case ev.Kind == agent.EventError:
		m.terminal.outcome = ansi.ProgramStateError
	case ev.Kind == agent.EventDone:
		m.terminal.outcome = ansi.ProgramStateDone
		if !m.terminal.focused {
			return tea.Raw(ansi.Notify("Bits finished"))
		}
	}
	return nil
}

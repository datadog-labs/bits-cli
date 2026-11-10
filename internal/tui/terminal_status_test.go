package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

func TestTerminalTitleAnimatesAndStops(t *testing.T) {
	m := newShell()
	m.mode, m.workspaceDisplayPath = ModeChat, "/work/project\x07\n"
	m.begin(opTurn, nil)
	m.watchRun(1, nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	first := m.View()
	m.Update(terminalTickMsg{})
	next := m.View()
	if first.WindowTitle == next.WindowTitle || !strings.HasSuffix(next.WindowTitle, " Bits | project") ||
		strings.ContainsAny(next.WindowTitle, "\x07\n") || next.ProgressBar == nil {
		t.Fatalf("title did not animate safely: %q -> %q", first.WindowTitle, next.WindowTitle)
	}
	m.syncApprovals([]agent.Block{awaitingCall("approval")})
	m.Update(nil)
	if view := m.View(); view.WindowTitle != "Bits | project" || view.ProgressBar != nil ||
		view.ProgramStatus.Kind != ansi.ProgramStatusKindPermission {
		t.Fatalf("approval view = %+v", view)
	}
	m.Update(engineEventMsg{ev: agent.Event{Run: 1, Kind: agent.EventDone}})
	_, cmd := m.Update(terminalTickMsg{}) // a pending tick cannot restart work
	if view := m.View(); view.WindowTitle != "Bits | project" || view.ProgressBar != nil || cmd != nil {
		t.Fatalf("completion title = %q, progress = %+v, command = %v", view.WindowTitle, view.ProgressBar, cmd != nil)
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got := m.View().WindowTitle; got != "Bits | project" {
		t.Fatalf("acknowledged title = %q", got)
	}
}

func TestTerminalOutcomesAndNotifications(t *testing.T) {
	for _, tt := range []struct {
		name    string
		kind    opKind
		event   agent.EventKind
		blurred bool
		want    ansi.ProgramState
		notify  bool
	}{
		{"focused completion", opTurn, agent.EventDone, false, ansi.ProgramStateDone, false},
		{"background completion", opTurn, agent.EventDone, true, ansi.ProgramStateDone, true},
		{"cancelled", opTurn, agent.EventCanceled, true, ansi.ProgramStateIdle, false},
		{"error", opTurn, agent.EventError, true, ansi.ProgramStateError, false},
		{"restore", opRestore, agent.EventDone, true, ansi.ProgramStateIdle, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newShell()
			m.mode = ModeChat
			if tt.blurred {
				m.Update(tea.BlurMsg{})
			}
			m.begin(tt.kind, nil)
			m.watchRun(1, nil)
			_, cmd := m.Update(engineEventMsg{ev: agent.Event{Run: 1, Kind: tt.event}})
			var notified bool
			for _, msg := range flattenMsgs(t, cmd) {
				if raw, ok := msg.(tea.RawMsg); ok && raw.Msg == ansi.Notify("Bits finished") {
					notified = true
				}
			}
			if got := m.View().ProgramStatus.State; got != tt.want || notified != tt.notify {
				t.Fatalf("state = %q, notified = %t; want %q, %t", got, notified, tt.want, tt.notify)
			}
		})
	}
}

func TestTerminalRenameLifecycle(t *testing.T) {
	m := newRenameModel(t, &tuiRenameBackend{resumeBackend: newResumeBackend()}, forkSourceID)
	m.startRename("")
	if got := m.View().ProgramStatus; got.State != ansi.ProgramStateBlocked || got.Kind != ansi.ProgramStatusKindQuestion {
		t.Fatalf("rename prompt status = %+v", got)
	}
	m.Update(tea.PasteMsg{Content: "New title"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.View().ProgramStatus.State; got != ansi.ProgramStateWorking {
		t.Fatalf("rename request status = %q", got)
	}
	// A rejected operation must not reset the admitted operation's outcome.
	m.terminal.outcome = ansi.ProgramStateError
	if m.begin(opTurn, nil) || m.terminal.outcome != ansi.ProgramStateError {
		t.Fatal("rejected turn changed the active operation")
	}
	m.terminal.outcome = ""
	applyRename(t, m, cmd)
	if got := m.View().ProgramStatus.State; got != ansi.ProgramStateIdle {
		t.Fatalf("completed rename status = %q", got)
	}
}

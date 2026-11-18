package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type fakeCommands struct {
	running int
	stopped int
	changed chan struct{}
}

func (f *fakeCommands) Running() int             { return f.running }
func (f *fakeCommands) Changed() <-chan struct{} { return f.changed }
func (f *fakeCommands) StopAll() int {
	f.stopped += f.running
	n := f.running
	f.running = 0
	return n
}

// The footer says how many commands run in the background, and leaving the
// conversation or /stop stops them.
func TestBackgroundCommandsShowAndStop(t *testing.T) {
	for _, test := range []struct {
		input, notice string
	}{
		{input: "/stop", notice: "Stopping 2 background commands."},
		{input: "/new", notice: "Started a new conversation. Stopped 2 background commands."},
	} {
		t.Run(test.input, func(t *testing.T) {
			m := newSizedChatModel(t)
			commands := &fakeCommands{running: 2, changed: make(chan struct{}, 1)}
			m.backgroundCommands.source = commands
			m.Update(backgroundCommandsChangedMsg{})
			if footer := ansi.Strip(m.chatFooter()); !strings.Contains(footer, "2 commands running") {
				t.Fatalf("footer = %q, want the running commands", footer)
			}

			setConversationInput(m, test.input)
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if commands.stopped != 2 {
				t.Fatalf("%s stopped %d commands, want 2", test.input, commands.stopped)
			}
			if view := ansi.Strip(m.View().Content); !strings.Contains(view, test.notice) {
				t.Fatalf("%s view lacks %q:\n%s", test.input, test.notice, view)
			}
			m.Update(backgroundCommandsChangedMsg{})
			if footer := ansi.Strip(m.chatFooter()); strings.Contains(footer, "running") {
				t.Fatalf("footer after %s = %q, want no running commands", test.input, footer)
			}
		})
	}
}

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

func newTurboModel(t *testing.T, mode string) *Model {
	t.Helper()
	m := New(agent.New(&turnBackend{t: t}, agent.Config{InferenceMode: mode}))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

// pumpState folds the state the engine published for a command, as the
// model's event pump does.
func pumpState(m *Model) {
	m.Update(waitEvent(m.events)())
}

func TestTurboSwitchesTheModeOfLaterTurns(t *testing.T) {
	for _, tc := range []struct {
		name, from, argument, want string
		notice                     chat.Notice
	}{
		{name: "on", argument: "on", want: assistant.InferenceModeFast, notice: chat.Notice{Level: chat.NoticeInfo, Text: "Turbo mode on."}},
		{name: "off", from: assistant.InferenceModeFast, argument: "off", notice: chat.Notice{Level: chat.NoticeInfo, Text: "Turbo mode off."}},
		{name: "off from another mode", from: "deep", argument: "off", notice: chat.Notice{Level: chat.NoticeInfo, Text: "Turbo mode off."}},
		{name: "already on", from: assistant.InferenceModeFast, argument: "on", want: assistant.InferenceModeFast},
		{name: "unknown", argument: "fast", notice: chat.Notice{Level: chat.NoticeError, Text: `Unknown turbo setting "fast". Use on or off.`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTurboModel(t, tc.from)
			_, _ = m.dispatchCommand("turbo", tc.argument)
			if got := latestNotice(m); got.Level != tc.notice.Level || got.Text != tc.notice.Text {
				t.Fatalf("notice = %#v, want %#v", got, tc.notice)
			}
			if got := m.engine.State().InferenceMode; got != tc.want {
				t.Fatalf("engine inference mode = %q, want %q", got, tc.want)
			}
		})
	}
}

// The footer and the picker follow the state the engine publishes.
func TestTurboShowsInFooterAndPicker(t *testing.T) {
	m := newTurboModel(t, "")
	_, _ = m.dispatchCommand("turbo", "")
	if view := screen(m); !strings.Contains(view, "› Standard (current)") || !strings.Contains(view, "Changes apply from the next turn.") {
		t.Fatalf("picker does not mark Standard current:\n%s", view)
	}
	press(m, tea.KeyDown, tea.KeyEnter)
	pumpState(m)
	if m.commandPrompt != nil || m.engine.State().InferenceMode != assistant.InferenceModeFast {
		t.Fatal("choosing Turbo did not switch the mode")
	}
	lines := strings.Split(screen(m), "\n")
	if footer := lines[len(lines)-1]; !strings.Contains(footer, "Turbo") {
		t.Fatalf("footer = %q, want Turbo shown", footer)
	}
	_, _ = m.dispatchCommand("turbo", "")
	if view := screen(m); !strings.Contains(view, "› Turbo (current)") {
		t.Fatalf("picker does not mark Turbo current:\n%s", view)
	}
}

func TestTurboDuringActiveTurnAppliesToTheNextTurn(t *testing.T) {
	m := newTurboModel(t, "")
	m.op = operation{kind: opTurn, run: 1}
	m.chatPhase = chat.PhaseStreaming
	_, _ = m.dispatchCommand("turbo", "on")
	if got := latestNotice(m).Text; got != "Turbo mode on. Applies from the next turn." {
		t.Fatalf("notice = %q, want the next turn named", got)
	}
	if m.chatPhase != chat.PhaseStreaming || m.op.run == 0 {
		t.Fatal("switching the mode disturbed the active turn")
	}
}

// A switch reads the engine, not the state it has yet to publish, so a quick
// second switch still applies.
func TestTurboSwitchesBackBeforeTheStateArrives(t *testing.T) {
	m := newTurboModel(t, "")
	_, _ = m.dispatchCommand("turbo", "on")
	_, _ = m.dispatchCommand("turbo", "off")
	if got := m.engine.InferenceMode(); got != "" {
		t.Fatalf("engine inference mode = %q, want Standard", got)
	}
	if got := latestNotice(m).Text; got != "Turbo mode off." {
		t.Fatalf("notice = %q, want the switch back announced", got)
	}
}

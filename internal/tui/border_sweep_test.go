package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

func TestBorderSweepArmsWhileWaitingOrStreaming(t *testing.T) {
	for _, phase := range []chat.Phase{chat.PhaseWaiting, chat.PhaseStreaming} {
		m := newShell()
		m.mode = ModeChat
		m.resize(80, 24)
		m.chatPhase = phase
		if cmd := m.syncBorderSweep(); cmd == nil {
			t.Fatalf("phase %v: syncBorderSweep() returned no command; the tick was never armed", phase)
		}
		if !m.borderSweepArmed {
			t.Errorf("phase %v: model should report the border sweep as armed", phase)
		}
	}
}

func TestBorderSweepStaysIdleOutsideWaitingOrStreaming(t *testing.T) {
	for _, phase := range []chat.Phase{chat.PhaseIdle, chat.PhaseLoading, chat.PhaseError} {
		m := newShell()
		m.mode = ModeChat
		m.resize(80, 24)
		m.chatPhase = phase
		if cmd := m.syncBorderSweep(); cmd != nil {
			t.Errorf("phase %v: syncBorderSweep() armed a tick outside Waiting/Streaming", phase)
		}
		if m.borderSweepArmed {
			t.Errorf("phase %v: model should not report the border sweep as armed", phase)
		}
	}
}

func TestBorderSweepArmsOnlyOnce(t *testing.T) {
	m := newShell()
	m.mode = ModeChat
	m.resize(80, 24)
	m.chatPhase = chat.PhaseWaiting
	if cmd := m.syncBorderSweep(); cmd == nil {
		t.Fatal("first sync should arm the tick")
	}
	if cmd := m.syncBorderSweep(); cmd != nil {
		t.Error("second sync armed a parallel tick chain")
	}
}

func TestBorderSweepTickAdvancesFrameAndRearms(t *testing.T) {
	m := newShell()
	m.mode = ModeChat
	m.resize(80, 24)
	// Production reaches this state through Init(), which focuses the editor
	// before any tick fires. Without it, the first m.Update() call here would
	// bundle reconcileFocus's own Focus() command into the result, muddying
	// assertions about what the border-sweep path alone returned.
	m.editor.Focus()
	m.chatPhase = chat.PhaseWaiting
	m.syncBorderSweep()

	_, cmd := m.Update(borderSweepTickMsg{generation: m.borderSweepGeneration})
	if cmd == nil {
		t.Fatal("a live tick should re-arm the chain")
	}
	if m.borderSweepFrame != 1 {
		t.Errorf("frame = %d, want 1", m.borderSweepFrame)
	}
}

func TestStaleBorderSweepTickIsDropped(t *testing.T) {
	m := newShell()
	m.mode = ModeChat
	m.resize(80, 24)
	// See TestBorderSweepTickAdvancesFrameAndRearms: focus the editor up front
	// so reconcileFocus's own Focus() command doesn't muddy the cmd assertion.
	m.editor.Focus()
	m.chatPhase = chat.PhaseWaiting
	m.syncBorderSweep()
	live := m.borderSweepGeneration

	_, cmd := m.Update(borderSweepTickMsg{generation: live - 1})
	if cmd != nil {
		t.Error("a stale tick should not re-arm the chain")
	}
	if m.borderSweepFrame != 0 {
		t.Errorf("frame = %d, want 0; a stale tick advanced the animation", m.borderSweepFrame)
	}
}

func TestBorderSweepDisarmsWhenPhaseReturnsToIdle(t *testing.T) {
	m := newShell()
	m.mode = ModeChat
	m.resize(80, 24)
	// See TestBorderSweepTickAdvancesFrameAndRearms: focus the editor up front
	// so reconcileFocus's own Focus() command doesn't muddy the cmd assertion.
	m.editor.Focus()
	m.chatPhase = chat.PhaseWaiting
	m.syncBorderSweep()
	m.Update(borderSweepTickMsg{generation: m.borderSweepGeneration})
	if m.borderSweepFrame == 0 {
		t.Fatal("expected the frame to have advanced")
	}
	stale := m.borderSweepGeneration

	m.chatPhase = chat.PhaseIdle
	if cmd := m.syncBorderSweep(); cmd != nil {
		t.Error("syncBorderSweep() re-armed a tick after returning to idle")
	}
	if m.borderSweepArmed {
		t.Error("border sweep should be disarmed once the phase is idle")
	}
	if m.borderSweepFrame != 0 {
		t.Errorf("frame = %d, want it reset to 0", m.borderSweepFrame)
	}
	if m.borderSweepGeneration == stale {
		t.Error("disarming should bump the generation so in-flight ticks are dropped")
	}
}

func TestBorderSweepIsIndependentOfToolAnimation(t *testing.T) {
	// Regression guard for the "must not reuse animation.go" requirement: a
	// tool-activity tick must not move the border-sweep frame, and vice versa.
	m := newShell()
	m.mode = ModeChat
	m.blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.turnEvents = make(chan agent.Event)
	m.resize(80, 24)
	// See TestBorderSweepTickAdvancesFrameAndRearms: focus the editor up front
	// so reconcileFocus's own Focus() command doesn't muddy the cmd assertions.
	m.editor.Focus()
	m.chatPhase = chat.PhaseWaiting

	m.syncAnimation()
	m.syncBorderSweep()

	m.Update(animTickMsg{generation: m.animGeneration})
	if m.borderSweepFrame != 0 {
		t.Error("a tool-activity tick advanced the border-sweep frame")
	}
	animFrameAfterAnimTick := m.animFrame
	if animFrameAfterAnimTick == 0 {
		t.Fatal("expected the tool-activity tick to have advanced animFrame")
	}

	m.Update(borderSweepTickMsg{generation: m.borderSweepGeneration})
	if m.animFrame != animFrameAfterAnimTick {
		t.Error("a border-sweep tick advanced the tool-activity frame")
	}
}

func TestSubmitArmsTheBorderSweep(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "new"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.editor.Focus()

	m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.borderSweepArmed {
		t.Fatal("submitting a message did not arm the border sweep")
	}
}

func TestTurnDoneDisarmsTheBorderSweep(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.chatPhase = chat.PhaseStreaming
	m.turnEvents = make(chan agent.Event)
	m.turnGen = 1
	m.syncBorderSweep()
	if !m.borderSweepArmed {
		t.Fatal("expected the border sweep to be armed while streaming")
	}

	m.Update(turnEventMsg{generation: 1, ev: agent.Event{Kind: agent.EventTurnDone}})

	if m.borderSweepArmed {
		t.Error("the border sweep is still armed after EventTurnDone")
	}
}

func TestNewConversationDisarmsTheBorderSweep(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.chatPhase = chat.PhaseWaiting
	m.syncBorderSweep()
	if !m.borderSweepArmed {
		t.Fatal("expected the border sweep to be armed")
	}

	m.dispatchCommand("new")

	if m.borderSweepArmed {
		t.Error("the idle /new path left the border sweep armed")
	}
}

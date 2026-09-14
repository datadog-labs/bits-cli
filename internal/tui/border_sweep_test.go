package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func sweepModel(phase chat.Phase) *Model {
	m := newShell()
	m.mode = ModeChat
	m.resize(80, 24)
	m.editor.Focus()
	m.turnEvents = make(chan agent.Event)
	m.chatPhase = phase
	return m
}

func TestBorderSweepArmsOnlyForActiveTurnPhases(t *testing.T) {
	for _, phase := range []chat.Phase{chat.PhaseWaiting, chat.PhaseStreaming} {
		m := sweepModel(phase)
		if cmd := m.syncAnimations(); cmd == nil || !m.borderSweepActive || !m.animationArmed {
			t.Errorf("phase %v did not start sweep and clock", phase)
		}
	}

	for _, phase := range []chat.Phase{chat.PhaseIdle, chat.PhaseLoading, chat.PhaseError} {
		m := sweepModel(phase)
		if cmd := m.syncAnimations(); cmd != nil || m.borderSweepActive || m.animationArmed {
			t.Errorf("phase %v started sweep outside active work", phase)
		}
	}
}

func TestBorderSweepRequiresLiveTurn(t *testing.T) {
	m := sweepModel(chat.PhaseStreaming)
	m.turnEvents = nil
	if cmd := m.syncAnimations(); cmd != nil || m.borderSweepActive {
		t.Fatal("stale streaming phase without a live turn started sweep")
	}
}

func TestBorderSweepPausesWhenEverythingWaitsForApproval(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	m.blocks = []agent.Block{animToolBlock(agent.ToolAwaitingApproval)}
	m.pendingApprovals = append([]agent.Block(nil), m.blocks...)
	m.refreshViewport()

	if cmd := m.syncAnimations(); cmd != nil || m.animationArmed || m.borderSweepActive {
		t.Fatal("approval-only wait started an animation clock")
	}
}

func TestBorderSweepContinuesWhenApprovalAndRunningToolCoexist(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	approval := animToolBlock(agent.ToolAwaitingApproval)
	running := animToolBlock(agent.ToolRunning)
	running.ID.Key = "call-2"
	m.blocks = []agent.Block{approval, running}
	m.pendingApprovals = []agent.Block{approval}
	m.refreshViewport()

	if cmd := m.syncAnimations(); cmd == nil {
		t.Fatal("parallel running tool did not start shared clock")
	}
	if !m.toolAnimationActive || !m.borderSweepActive {
		t.Fatalf("tool=%v sweep=%v, want both active", m.toolAnimationActive, m.borderSweepActive)
	}
}

func TestWithoutMotionDisablesSweepButNotToolAnimation(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	m.blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.refreshViewport()
	m.applyStyles(styles.Default(true).WithoutMotion())

	if cmd := m.syncAnimations(); cmd == nil {
		t.Fatal("running tool should still start the shared clock")
	}
	if m.borderSweepActive {
		t.Fatal("WithoutMotion left border sweep active")
	}
	if !m.toolAnimationActive || !m.animationArmed {
		t.Fatal("WithoutMotion disabled glyph-based tool activity")
	}
}

func TestWithoutMotionKeepsSweepOnlyClockIdle(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	m.applyStyles(styles.Default(true).WithoutMotion())
	if cmd := m.syncAnimations(); cmd != nil || m.animationArmed {
		t.Fatal("motion-disabled sweep started a repaint clock")
	}
}

func TestSweepStopsOutsideChatAndRestartsOnReturn(t *testing.T) {
	m := sweepModel(chat.PhaseStreaming)
	m.syncAnimations()
	m.setMode(ModeStatus)
	m.Update(struct{}{})
	if m.animationArmed || m.borderSweepActive {
		t.Fatal("hidden composer kept sweep clock running")
	}

	m.setMode(ModeChat)
	m.Update(struct{}{})
	if !m.animationArmed || !m.borderSweepActive {
		t.Fatal("returning to chat did not restart sweep")
	}
}

func TestSweepStopsAtApprovalMinimumSizeAndRestarts(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	running := animToolBlock(agent.ToolRunning)
	approval := animToolBlock(agent.ToolAwaitingApproval)
	approval.ID.Key = "approval"
	m.blocks = []agent.Block{running, approval}
	m.pendingApprovals = []agent.Block{approval}
	m.refreshViewport()
	m.syncAnimations()

	m.Update(tea.WindowSizeMsg{Width: minimumApprovalWidth - 1, Height: minimumApprovalHeight})
	if m.animationArmed {
		t.Fatal("hidden approval/chat view kept shared clock running")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.animationArmed || !m.borderSweepActive || !m.toolAnimationActive {
		t.Fatal("resizing back did not restart animations")
	}
}

func TestStoppingLastAnimationInvalidatesSharedClock(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	start := time.Unix(100, 0)
	m.syncAnimationsAt(start)
	m.advanceAnimations(animationTickMsg{generation: m.animationGeneration, at: start.Add(100 * time.Millisecond)})
	stale := m.animationGeneration
	m.chatPhase = chat.PhaseIdle
	m.syncAnimationsAt(start.Add(time.Second))

	if m.animationArmed || m.borderSweepActive || m.borderSweepFrame != 0 {
		t.Fatal("idle phase did not reset and disarm sweep")
	}
	if m.animationGeneration == stale {
		t.Fatal("disarm did not invalidate pending tick")
	}
}

func TestSubmitArmsSharedClockForSweep(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "new"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.editor.Focus()
	m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.animationArmed || !m.borderSweepActive {
		t.Fatal("submitting a message did not arm the sweep clock")
	}
}

func TestTurnDoneDisarmsSweepClock(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.chatPhase = chat.PhaseStreaming
	m.turnEvents = make(chan agent.Event)
	m.turnGen = 1
	m.syncAnimations()
	m.Update(turnEventMsg{generation: 1, ev: agent.Event{Kind: agent.EventTurnDone}})

	if m.animationArmed || m.borderSweepActive {
		t.Fatal("EventTurnDone left the sweep clock armed")
	}
}

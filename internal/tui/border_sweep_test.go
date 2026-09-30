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
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.Focus()
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.chatPhase = phase
	return m
}

func TestBorderSweepArmsOnlyForActiveTurnPhases(t *testing.T) {
	for _, phase := range []chat.Phase{chat.PhaseWaiting, chat.PhaseStreaming} {
		m := sweepModel(phase)
		if cmd := m.syncAnimations(); cmd == nil || !m.animBorderSweep.active || !m.animClock.armed {
			t.Errorf("phase %v did not start sweep and clock", phase)
		}
	}

	for _, phase := range []chat.Phase{chat.PhaseIdle, chat.PhaseLoading, chat.PhaseError} {
		m := sweepModel(phase)
		if cmd := m.syncAnimations(); cmd != nil || m.animBorderSweep.active || m.animClock.armed {
			t.Errorf("phase %v started sweep outside active work", phase)
		}
	}
}

func TestBorderSweepRequiresLiveTurn(t *testing.T) {
	m := sweepModel(chat.PhaseStreaming)
	m.op = operation{}
	if cmd := m.syncAnimations(); cmd != nil || m.animBorderSweep.active {
		t.Fatal("stale streaming phase without a live turn started sweep")
	}
}

func TestBorderSweepPausesWhenEverythingWaitsForApproval(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	m.transcript.Blocks = []agent.Block{animToolBlock(agent.ToolAwaitingApproval)}
	m.pendingApprovals = append([]agent.Block(nil), m.transcript.Blocks...)
	m.syncTranscript()

	if cmd := m.syncAnimations(); cmd != nil || m.animClock.armed || m.animBorderSweep.active {
		t.Fatal("approval-only wait started an animation clock")
	}
}

func TestBorderSweepContinuesWhenApprovalAndRunningToolCoexist(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	approval := animToolBlock(agent.ToolAwaitingApproval)
	running := animToolBlock(agent.ToolRunning)
	running.ID.Key = "call-2"
	m.transcript.Blocks = []agent.Block{approval, running}
	m.pendingApprovals = []agent.Block{approval}
	m.syncTranscript()

	if cmd := m.syncAnimations(); cmd == nil {
		t.Fatal("parallel running tool did not start shared clock")
	}
	if !m.animActivity.active || !m.animBorderSweep.active {
		t.Fatalf("tool=%v sweep=%v, want both active", m.animActivity.active, m.animBorderSweep.active)
	}
}

func TestWithoutMotionKeepsAnimationClockIdle(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	m.transcript.Blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.syncTranscript()
	m.applyStyles(styles.Default(true).WithoutMotion())

	if cmd := m.syncAnimations(); cmd != nil {
		t.Fatal("motion-disabled visuals started the shared clock")
	}
	if m.animBorderSweep.active {
		t.Fatal("WithoutMotion left border sweep active")
	}
	if m.animActivity.active || m.animClock.armed {
		t.Fatal("WithoutMotion kept tool animation active")
	}
}

func TestWithoutMotionKeepsSweepOnlyClockIdle(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	m.applyStyles(styles.Default(true).WithoutMotion())
	if cmd := m.syncAnimations(); cmd != nil || m.animClock.armed {
		t.Fatal("motion-disabled sweep started a repaint clock")
	}
}

func TestSweepStopsOutsideChatAndRestartsOnReturn(t *testing.T) {
	m := sweepModel(chat.PhaseStreaming)
	m.syncAnimations()
	m.setMode(ModeStatus)
	m.Update(struct{}{})
	if m.animClock.armed || m.animBorderSweep.active {
		t.Fatal("hidden composer kept sweep clock running")
	}

	m.setMode(ModeChat)
	m.Update(struct{}{})
	if !m.animClock.armed || !m.animBorderSweep.active {
		t.Fatal("returning to chat did not restart sweep")
	}
}

func TestSweepStopsAtApprovalMinimumSizeAndRestarts(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	running := animToolBlock(agent.ToolRunning)
	approval := animToolBlock(agent.ToolAwaitingApproval)
	approval.ID.Key = "approval"
	m.transcript.Blocks = []agent.Block{running, approval}
	m.pendingApprovals = []agent.Block{approval}
	m.syncTranscript()
	m.syncAnimations()

	m.Update(tea.WindowSizeMsg{Width: minimumApprovalWidth - 1, Height: minimumApprovalHeight})
	if m.animClock.armed {
		t.Fatal("hidden approval/chat view kept shared clock running")
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.animClock.armed || !m.animBorderSweep.active || !m.animActivity.active {
		t.Fatal("resizing back did not restart animations")
	}
}

func TestStoppingLastAnimationInvalidatesSharedClock(t *testing.T) {
	m := sweepModel(chat.PhaseWaiting)
	start := time.Unix(100, 0)
	m.syncAnimationsAt(start)
	m.advanceAnimations(animationTickMsg{generation: m.animClock.generation, at: start.Add(100 * time.Millisecond)})
	stale := m.animClock.generation
	m.chatPhase = chat.PhaseIdle
	m.syncAnimationsAt(start.Add(time.Second))

	if m.animClock.armed || m.animBorderSweep.active || m.animBorderSweep.frame != 0 {
		t.Fatal("idle phase did not reset and disarm sweep")
	}
	if m.animClock.generation == stale {
		t.Fatal("disarm did not invalidate pending tick")
	}
}

func TestSubmitArmsSharedClockForSweep(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "new"}))
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.Focus()
	m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.animClock.armed || !m.animBorderSweep.active {
		t.Fatal("submitting a message did not arm the sweep clock")
	}
}

func TestTurnDoneDisarmsSweepClock(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.chatPhase = chat.PhaseStreaming
	m.op = operation{kind: opTurn, gen: 1, events: make(chan agent.Event)}
	m.syncAnimations()
	m.Update(turnEventMsg{generation: 1, ev: agent.Event{Kind: agent.EventTurnDone}})

	if m.animClock.armed || m.animBorderSweep.active {
		t.Fatal("EventTurnDone left the sweep clock armed")
	}
}

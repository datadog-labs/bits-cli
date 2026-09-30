package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

func animToolBlock(status agent.ToolStatus) agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: "search_logs", Status: status},
	}
}

func animModel(status agent.ToolStatus) *Model {
	return animModelWithBlock(animToolBlock(status))
}

func animModelWithBlock(block agent.Block) *Model {
	m := newShell()
	m.mode = ModeChat
	m.transcript.Blocks = []agent.Block{block}
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.syncTranscript()
	m.editor.Focus()
	return m
}

func TestAnimationArmsWhileActivityInFlight(t *testing.T) {
	blocks := []agent.Block{
		animToolBlock(agent.ToolRunning),
		{ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "thinking", Kind: assistant.KindReasoning}, Kind: assistant.KindReasoning, Thinking: &assistant.ThinkingPayload{Content: "plan"}},
	}
	for _, block := range blocks {
		m := animModelWithBlock(block)
		if cmd := m.syncAnimations(); cmd == nil {
			t.Fatal("syncAnimations() returned no command; the clock was never armed")
		}
		if !m.animClock.armed {
			t.Error("model should report the animation as armed")
		}
	}
}

func TestAnimationClockUsesThirtyFPSRepaintCadence(t *testing.T) {
	if animationInterval < 30*time.Millisecond || animationInterval > 35*time.Millisecond {
		t.Fatalf("animationInterval = %s, want approximately 30 FPS", animationInterval)
	}
}

func TestAnimationRepaintCadenceFollowsActiveVisuals(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimations()
	if got := m.animationRepaintInterval(); got != toolAnimInterval {
		t.Fatalf("tool-only repaint interval = %s, want %s", got, toolAnimInterval)
	}

	m.chatPhase = chat.PhaseWaiting
	m.syncAnimations()
	if got := m.animationRepaintInterval(); got != animationInterval {
		t.Fatalf("sweep repaint interval = %s, want %s", got, animationInterval)
	}
}

func TestAnimationClockArmsOnceForBothAnimations(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.chatPhase = chat.PhaseWaiting
	start := time.Unix(100, 0)

	if cmd := m.syncAnimationsAt(start); cmd == nil {
		t.Fatal("first sync did not arm the shared clock")
	}
	if !m.animClock.armed || !m.animTool.active || !m.animBorderSweep.active {
		t.Fatalf("armed=%v tool=%v sweep=%v, want all true", m.animClock.armed, m.animTool.active, m.animBorderSweep.active)
	}
	if cmd := m.syncAnimationsAt(start); cmd != nil {
		t.Fatal("second sync started a parallel clock chain")
	}
}

func TestAnimationClockDerivesIndependentLogicalFrames(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.chatPhase = chat.PhaseWaiting
	start := time.Unix(100, 0)
	m.syncAnimationsAt(start)

	cmd := m.advanceAnimations(animationTickMsg{
		generation: m.animClock.generation,
		at:         start.Add(100 * time.Millisecond),
	})
	if cmd == nil {
		t.Fatal("live clock tick did not re-arm")
	}
	if m.animTool.frame != 2 {
		t.Errorf("tool frame = %d, want 2 at 100ms", m.animTool.frame)
	}
	if m.animBorderSweep.frame != 5 {
		t.Errorf("sweep frame = %d, want 5 at 100ms", m.animBorderSweep.frame)
	}
}

func TestStaleAnimationClockTickIsDropped(t *testing.T) {
	m := animModel(agent.ToolRunning)
	start := time.Unix(100, 0)
	m.syncAnimationsAt(start)
	live := m.animClock.generation

	if cmd := m.advanceAnimations(animationTickMsg{generation: live - 1, at: start.Add(time.Second)}); cmd != nil {
		t.Fatal("stale tick re-armed its clock chain")
	}
	if m.animTool.frame != 0 {
		t.Fatalf("stale tick advanced tool frame to %d", m.animTool.frame)
	}
}

func TestAnimationClockDisarmsAfterClosedTurnWithStaleTool(t *testing.T) {
	m := animModel(agent.ToolRunning)
	start := time.Unix(100, 0)
	m.syncAnimationsAt(start)
	m.advanceAnimations(animationTickMsg{generation: m.animClock.generation, at: start.Add(100 * time.Millisecond)})
	staleGeneration := m.animClock.generation

	m.op = operation{}
	if cmd := m.syncAnimationsAt(start.Add(time.Second)); cmd != nil {
		t.Fatal("disarming unexpectedly returned a command")
	}
	if m.animClock.armed || m.animTool.active {
		t.Fatal("closed turn left the animation clock armed")
	}
	if m.animTool.frame != 0 {
		t.Fatalf("tool frame = %d after disarm, want 0", m.animTool.frame)
	}
	if m.animClock.generation == staleGeneration {
		t.Fatal("disarming did not invalidate the live tick chain")
	}
	if !m.list.HasAnimated() {
		t.Fatal("test setup invalid: stale tool should still report running")
	}
}

func TestTurnClosedUpdateDisarmsDespiteStaleRunningBlock(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimations()
	if !m.animClock.armed {
		t.Fatal("test setup did not arm the clock")
	}

	m.Update(turnClosedMsg{generation: m.op.gen})
	if m.animClock.armed || m.animTool.active || m.animBorderSweep.active {
		t.Fatal("turnClosedMsg left animations active")
	}
	if !m.list.HasAnimated() {
		t.Fatal("test setup invalid: running block should remain stale")
	}
}

func TestPendingNewClearsStaleRunningBlockAndDisarms(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.transcript.Blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.syncTranscript()
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.op.then = thenNewConversation
	m.syncAnimations()

	m.Update(turnClosedMsg{generation: m.op.gen})
	if m.animClock.armed || m.animTool.active || m.animBorderSweep.active {
		t.Fatal("pending /new left animations active")
	}
	if m.list.HasAnimated() || len(m.transcript.Blocks) != 0 {
		t.Fatal("pending /new did not clear stale running transcript")
	}
}

func TestIdleNewAfterClosedStaleTurnRemainsDisarmed(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.transcript.Blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.syncTranscript()
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.syncAnimations()
	m.Update(turnClosedMsg{generation: m.op.gen})

	m.dispatchCommand("new", "")
	// dispatchCommand is normally reached from Update; run the centralized
	// reconciliation that Update performs after dispatch.
	m.Update(struct{}{})
	if m.animClock.armed || m.list.HasAnimated() {
		t.Fatal("idle /new re-armed animation from stale transcript state")
	}
}

func TestConversationSwitchWithPersistedRunningBlockStaysDisarmed(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.transcript.Blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.syncTranscript()
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.syncAnimations()
	m.Update(turnClosedMsg{generation: m.op.gen})

	m.dispatchCommand("resume", "")
	if m.mode != ModeConversations {
		t.Fatal("test setup did not open conversation picker")
	}
	generation := m.conversationTask.gen
	m.applyConversationSwitchResult(conversationSwitchResultMsg{
		generation: generation,
		result: agent.ConversationSwitchResult{
			ConversationID: "other",
			Blocks:         []agent.Block{animToolBlock(agent.ToolRunning)},
		},
	})
	m.Update(struct{}{})

	if m.animClock.armed || m.animTool.active {
		t.Fatal("persisted running block armed clock without a live turn")
	}
	if !m.list.HasAnimated() {
		t.Fatal("test setup invalid: switched transcript should report running")
	}
}

func TestAnimationClockStaysIdleWithoutVisibleRunningWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
	}{
		{"awaiting approval", agent.ToolAwaitingApproval},
		{"success", agent.ToolSuccess},
		{"error", agent.ToolError},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := animModel(test.status)
			if cmd := m.syncAnimations(); cmd != nil || m.animClock.armed {
				t.Fatal("clock armed without running work")
			}
		})
	}
}

func TestAnimationClockStopsWhileChatIsHiddenAndRestarts(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimations()
	if !m.animClock.armed {
		t.Fatal("clock did not start for visible running tool")
	}

	m.setMode(ModeStatus)
	m.Update(struct{}{})
	if m.animClock.armed || m.animTool.active {
		t.Fatal("hidden status view kept the animation clock running")
	}

	m.setMode(ModeChat)
	m.Update(struct{}{})
	if !m.animClock.armed || !m.animTool.active {
		t.Fatal("returning to chat did not restart tool animation")
	}
}

func TestAnimationClockStopsWhileChatIsTooSmallAndRestarts(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimations()

	m.Update(tea.WindowSizeMsg{Width: minimumChatWidth - 1, Height: minimumChatHeight})
	if m.animClock.armed {
		t.Fatal("too-small chat kept the animation clock running")
	}

	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.animClock.armed {
		t.Fatal("resizing back did not restart the animation clock")
	}
}

func TestColorProfileDoesNotDisableToolMotion(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.ASCII, colorprofile.ANSI, colorprofile.ANSI256, colorprofile.TrueColor} {
		t.Run(profile.String(), func(t *testing.T) {
			m := animModel(agent.ToolRunning)
			m.Update(tea.ColorProfileMsg{Profile: profile})
			if !m.animTool.active {
				t.Fatal("color profile disabled compact tool motion")
			}
		})
	}
}

func TestAnimationFrameReachesTheList(t *testing.T) {
	m := animModel(agent.ToolRunning)
	start := time.Unix(100, 0)
	m.syncAnimationsAt(start)
	first := m.list.Render()
	m.advanceAnimations(animationTickMsg{generation: m.animClock.generation, at: start.Add(250 * time.Millisecond)})
	if m.list.Render() == first {
		t.Fatal("advancing the logical tool frame did not change the transcript")
	}
}

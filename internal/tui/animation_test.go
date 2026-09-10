package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

// animToolBlock builds a tool block in the given status.
func animToolBlock(status agent.ToolStatus) agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "call-1"},
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{Name: "search_logs", Status: status},
	}
}

// animModel returns a chat-mode model with an active turn whose transcript
// holds one tool block in the given status. turnEvents is non-nil (a fake,
// never-closed channel) because production only ever puts a block in an
// in-flight status while a turn is active — the tests exercising the closed,
// stale-block case set it back to nil explicitly.
func animModel(status agent.ToolStatus) *Model {
	m := newShell()
	m.mode = ModeChat
	m.blocks = []agent.Block{animToolBlock(status)}
	m.turnEvents = make(chan agent.Event)
	// resize is what propagates the width to the transcript list; setting the
	// model's fields alone leaves the list at width 0, rendering nothing.
	m.resize(80, 24)
	// Production reaches this state through Init(), which focuses the editor
	// before any tick fires. Without it, the first m.Update() call here would
	// bundle reconcileFocus's own Focus() command into the result, muddying
	// assertions about what the animation path alone returned.
	m.editor.Focus()
	return m
}

func TestAnimationArmsWhileToolInFlight(t *testing.T) {
	m := animModel(agent.ToolRunning)
	if cmd := m.syncAnimation(); cmd == nil {
		t.Fatal("syncAnimation() returned no command; the tick was never armed")
	}
	if !m.animArmed {
		t.Error("model should report the animation as armed")
	}
}

// TestAnimationStaysIdleWhenNothingIsInFlight is the cost guarantee: a
// transcript with no live tool must not schedule repaints.
func TestAnimationStaysIdleWhenNothingIsInFlight(t *testing.T) {
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
			if cmd := m.syncAnimation(); cmd != nil {
				t.Error("syncAnimation() armed a tick with no in-flight tool")
			}
			if m.animArmed {
				t.Error("model should not report the animation as armed")
			}
		})
	}
}

// TestAnimationArmsOnlyOnce keeps a second sync from starting a parallel tick
// chain, which would advance the frame twice per interval and animate at
// double speed.
func TestAnimationArmsOnlyOnce(t *testing.T) {
	m := animModel(agent.ToolRunning)
	if cmd := m.syncAnimation(); cmd == nil {
		t.Fatal("first sync should arm the tick")
	}
	if cmd := m.syncAnimation(); cmd != nil {
		t.Error("second sync armed a parallel tick chain")
	}
}

func TestAnimationTickAdvancesFrameAndRearms(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimation()

	_, cmd := m.Update(animTickMsg{generation: m.animGeneration})
	if cmd == nil {
		t.Fatal("a live tick should re-arm the chain")
	}
	if m.animFrame != 1 {
		t.Errorf("frame = %d, want 1", m.animFrame)
	}
}

// TestStaleAnimationTickIsDropped is the generation guard: a tick left over
// from a superseded chain must die rather than advance the frame.
func TestStaleAnimationTickIsDropped(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimation()
	live := m.animGeneration

	_, cmd := m.Update(animTickMsg{generation: live - 1})
	if cmd != nil {
		t.Error("a stale tick should not re-arm the chain")
	}
	if m.animFrame != 0 {
		t.Errorf("frame = %d, want 0; a stale tick advanced the animation", m.animFrame)
	}
}

// TestAnimationDisarmsWhenToolSettles stops the repaint loop once the last
// tool finishes, and resets the frame so the next run starts from a clean
// sweep.
func TestAnimationDisarmsWhenToolSettles(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimation()
	m.Update(animTickMsg{generation: m.animGeneration})
	if m.animFrame == 0 {
		t.Fatal("expected the frame to have advanced")
	}
	stale := m.animGeneration

	m.blocks = []agent.Block{animToolBlock(agent.ToolSuccess)}
	m.refreshViewport()
	if cmd := m.syncAnimation(); cmd != nil {
		t.Error("syncAnimation() re-armed a tick after the tool settled")
	}
	if m.animArmed {
		t.Error("animation should be disarmed once nothing is in flight")
	}
	if m.animFrame != 0 {
		t.Errorf("frame = %d, want it reset to 0", m.animFrame)
	}
	if m.animGeneration == stale {
		t.Error("disarming should bump the generation so in-flight ticks are dropped")
	}
}

// TestColorProfileDoesNotDisableGlyphMotion documents that compact tool
// motion is a spinner and fixed-width dots, not a color gradient.
func TestColorProfileDoesNotDisableGlyphMotion(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.ASCII, colorprofile.ANSI, colorprofile.ANSI256, colorprofile.TrueColor} {
		t.Run(profile.String(), func(t *testing.T) {
			m := animModel(agent.ToolRunning)
			m.Update(tea.ColorProfileMsg{Profile: profile})
			if cmd := m.syncAnimation(); cmd == nil {
				t.Error("color profile disabled compact tool motion")
			}
		})
	}
}

// TestAnimationFrameReachesTheList closes the loop: the counter the tick
// advances must be the one the transcript renders with.
func TestAnimationFrameReachesTheList(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimation()
	first := m.list.Render()

	for range 5 {
		m.Update(animTickMsg{generation: m.animGeneration})
	}
	if m.list.Render() == first {
		t.Error("advancing the animation did not change the rendered transcript")
	}
}

// TestConversationResetDisarmsTheAnimation guards the ordering around /new, on
// the real path rather than by calling syncAnimation by hand.
//
// A cancelled client tool can leave its block reporting ToolRunning, so a sync
// evaluated before the reset sees no change and leaves the chain armed; the
// reset then empties the transcript and nothing re-syncs, so the tick keeps
// re-arming itself forever against an empty view. Syncing after the reset
// cannot get this wrong: the list is empty, so it always disarms.
func TestConversationResetDisarmsTheAnimation(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.refreshViewport()
	m.turnEvents = make(chan agent.Event) // the active turn the running tool belongs to
	m.syncAnimation()
	if !m.animArmed {
		t.Fatal("expected the tick to be armed")
	}

	// /new completing as the turn's channel closes, with the tool never settled.
	events := make(chan agent.Event)
	close(events)
	m.turnEvents = events
	m.pendingNew = true

	m.Update(turnClosedMsg{generation: m.turnGen})

	if m.animArmed {
		t.Error("the tick is still armed after /new cleared the transcript")
	}
	if m.list.HasAnimated() {
		t.Error("the transcript should be empty after the reset")
	}
}

// TestAnimationDisarmsWhenTurnClosesDespiteStaleBlock is the bug a review
// comment on #44 identified: the engine's tool loop exits on ctx.Done() without
// settling its block (see the "Known gap" comment in update.go), so after a
// Ctrl+C the transcript can still report ToolRunning. syncAnimation must not
// let that stale block keep the tick armed once the turn itself has actually
// closed — turnEvents going nil is the authoritative signal, not the block
// state, which the engine does not guarantee to be accurate.
func TestAnimationDisarmsWhenTurnClosesDespiteStaleBlock(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.syncAnimation()
	if !m.animArmed {
		t.Fatal("expected the tick to be armed while the tool block reports running")
	}

	// The turn closes (Ctrl+C, channel drained) without the engine settling the
	// stale ToolRunning block — the known gap.
	m.turnEvents = nil

	if cmd := m.syncAnimation(); cmd != nil {
		t.Error("syncAnimation re-armed a tick after the turn closed")
	}
	if m.animArmed {
		t.Error("the tick is still armed after the turn closed, despite the stale block")
	}
	if !m.list.HasAnimated() {
		t.Fatal("test setup invalid: the block should still (incorrectly) report running")
	}
}

// TestEscCancelThenIdleNewLeavesAnimationDisarmed drives the full scenario a
// review comment on #44 raised: a client tool is cancelled with Esc, its block
// is never settled by the engine (the known gap), the turn closes, and only
// then does the user run /new while genuinely idle — dispatchCommand's "active"
// check requires turnEvents == nil, so this reaches startNewConversation()
// directly rather than the pendingNew path.
//
// It is end to end through Update rather than by calling syncAnimation by
// hand: it is the turn's own turnClosedMsg — not this test — that must leave
// animArmed false, because that is what makes the later idle /new benign.
func TestEscCancelThenIdleNewLeavesAnimationDisarmed(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.refreshViewport()
	m.turnEvents = make(chan agent.Event)
	m.cancelTurn = func() {}
	m.syncAnimation()
	if !m.animArmed {
		t.Fatal("expected the tick to be armed while the tool is running")
	}

	// Esc: cancelRemote() marks the cancellation; the channel closing (and the
	// resulting turnClosedMsg) is what actually happens next, asynchronously.
	m.cancelRemote()

	// The turn closes without the engine ever settling the block.
	m.Update(turnClosedMsg{generation: m.turnGen})
	if m.animArmed {
		t.Fatal("the tick should have disarmed the moment the turn closed")
	}
	if !m.list.HasAnimated() {
		t.Fatal("test setup invalid: the block should still (incorrectly) report running")
	}

	// Only now is the model genuinely idle, so /new reaches startNewConversation
	// directly rather than through the pendingNew/cancel flow.
	if m.turnEvents != nil || m.cancelTurn != nil {
		t.Fatal("test setup invalid: dispatchCommand's active check requires both nil")
	}
	m.dispatchCommand("new")

	if m.animArmed {
		t.Error("the idle /new path left the tick armed")
	}
	if m.list.HasAnimated() {
		t.Error("the transcript should be empty after /new")
	}
}

// TestConversationSwitchLeavesAnimationDisarmed drives the picker path a review
// comment on #44 raised: applyConversationSwitchResult replaces m.blocks and
// m.list wholesale without calling syncAnimation. /resume can only open the
// picker while idle (commandRejectedDuringTurn), so once the earlier turn has
// actually closed — the same disarm point TestEscCancelThenIdleNewLeavesAnimationDisarmed
// exercises — nothing in the picker's own flow can re-arm the tick, since no
// turn pump exists while m.mode == ModeConversations. This proves that
// invariant holds across a switch into a transcript that itself contains a
// stale-but-never-live "running" block.
func TestConversationSwitchLeavesAnimationDisarmed(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.blocks = []agent.Block{animToolBlock(agent.ToolRunning)}
	m.refreshViewport()
	m.turnEvents = make(chan agent.Event)
	m.syncAnimation()
	if !m.animArmed {
		t.Fatal("expected the tick to be armed while the tool is running")
	}
	m.Update(turnClosedMsg{generation: m.turnGen})
	if m.animArmed {
		t.Fatal("expected the tick to have disarmed when the turn closed")
	}

	// /resume: only reachable while idle, per its commandRejectedDuringTurn
	// policy — matching the precondition dispatchCommand itself enforces.
	m.dispatchCommand("resume")
	if m.mode != ModeConversations {
		t.Fatal("test setup invalid: expected the picker to open")
	}

	// The switch resolves into a conversation whose own persisted history still
	// contains a tool call with no result — genuinely "running" data, but with
	// no live turnEvents pump backing it from this client.
	generation := m.conversationGeneration
	m.applyConversationSwitchResult(conversationSwitchResultMsg{
		generation: generation,
		result: agent.ConversationSwitchResult{
			ConversationID: "other",
			Blocks:         []agent.Block{animToolBlock(agent.ToolRunning)},
		},
	})

	if m.animArmed {
		t.Error("the tick is armed after switching conversations with no active turn")
	}
}

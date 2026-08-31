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

// animModel returns a chat-mode model whose transcript holds one tool block in
// the given status.
func animModel(status agent.ToolStatus) *Model {
	m := newShell()
	m.mode = ModeChat
	m.blocks = []agent.Block{animToolBlock(status)}
	// resize is what propagates the width to the transcript list; setting the
	// model's fields alone leaves the list at width 0, rendering nothing.
	m.resize(80, 24)
	return m
}

func TestAnimationArmsWhileToolInFlight(t *testing.T) {
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
	}{
		{"running", agent.ToolRunning},
		{"awaiting approval", agent.ToolAwaitingApproval},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := animModel(test.status)
			if cmd := m.syncAnimation(); cmd == nil {
				t.Fatal("syncAnimation() returned no command; the tick was never armed")
			}
			if !m.animArmed {
				t.Error("model should report the animation as armed")
			}
		})
	}
}

// TestAnimationStaysIdleWhenNothingIsInFlight is the cost guarantee: a
// transcript with no live tool must not schedule repaints.
func TestAnimationStaysIdleWhenNothingIsInFlight(t *testing.T) {
	for _, test := range []struct {
		name   string
		status agent.ToolStatus
	}{
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

// TestLowColorProfileDisablesMotion is the graceful degradation: lipgloss
// always renders truecolor, so a 16-color terminal is detected here and the
// theme is flattened rather than animated into mush.
func TestLowColorProfileDisablesMotion(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.ASCII, colorprofile.NoTTY} {
		t.Run(profile.String(), func(t *testing.T) {
			m := animModel(agent.ToolRunning)
			m.Update(tea.ColorProfileMsg{Profile: profile})

			if m.chatStyles.StatusRunningLabel.Len() != 0 {
				t.Error("theme should have been flattened for a terminal that cannot render a gradient")
			}
			if cmd := m.syncAnimation(); cmd != nil {
				t.Error("no tick should be armed when motion is disabled")
			}
		})
	}
}

// TestGradientCapableProfileKeepsMotion is the counterpart: a capable
// terminal must keep the animation.
func TestGradientCapableProfileKeepsMotion(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.ANSI256, colorprofile.TrueColor} {
		t.Run(profile.String(), func(t *testing.T) {
			m := animModel(agent.ToolRunning)
			m.Update(tea.ColorProfileMsg{Profile: profile})

			if m.chatStyles.StatusRunningLabel.Len() == 0 {
				t.Error("theme should keep its animated labels on a gradient-capable terminal")
			}
			if cmd := m.syncAnimation(); cmd == nil {
				t.Error("the tick should arm on a gradient-capable terminal")
			}
		})
	}
}

// TestUnknownColorProfileStaysOptimistic keeps the animation working when the
// terminal never reports a usable profile; Unknown means "not detected", not
// "incapable".
func TestUnknownColorProfileStaysOptimistic(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.Unknown})

	if m.chatStyles.StatusRunningLabel.Len() == 0 {
		t.Error("an undetected profile should not disable motion")
	}
}

// TestThemeSwitchPreservesDisabledMotion guards the interaction between the
// two theme inputs: re-deriving styles for a background change must not
// resurrect an animation the color profile ruled out.
func TestThemeSwitchPreservesDisabledMotion(t *testing.T) {
	m := animModel(agent.ToolRunning)
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI})
	m.setDarkBackground(!m.styles.IsDark)

	if m.chatStyles.StatusRunningLabel.Len() != 0 {
		t.Error("a background change re-enabled motion that the color profile disabled")
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

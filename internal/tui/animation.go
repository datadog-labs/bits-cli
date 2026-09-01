package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// animInterval is one animation frame. 20fps reads as smooth motion for the
// swept status chip; only in-flight tool blocks are re-rendered per frame, so
// the cost does not grow with the transcript.
//
// Open question for review: the login screen's spinner runs at 90ms (~11fps).
// A shimmer needs a higher rate than a glyph spinner to read as a sweep rather
// than a stutter, which is why these differ — but if one shared rate is
// preferable, this is the constant to change.
const animInterval = 50 * time.Millisecond

// minMotionProfile is the least capable color profile that can still render a
// gradient. lipgloss always emits truecolor and the terminal's real profile is
// applied later, at the output writer, so a sweep on a 16-color terminal would
// collapse to a few flat steps and read as a glitch rather than motion.
const minMotionProfile = colorprofile.ANSI256

// animTickMsg advances the status-chip animation. generation identifies the
// armed tick chain: arming and disarming both bump it, so a tick left over
// from a superseded chain is dropped instead of advancing the frame a second
// time per interval. Without this, re-arming while a chain is still live would
// animate at double speed.
type animTickMsg struct{ generation uint64 }

// animTick schedules the next frame, stamped with the chain it belongs to.
func animTick(generation uint64) tea.Cmd {
	return tea.Tick(animInterval, func(time.Time) tea.Msg {
		return animTickMsg{generation: generation}
	})
}

// syncAnimation arms or disarms the animation tick to match the transcript. It
// returns a command only when it starts a chain, so calling it on every
// transcript change is safe and idempotent.
//
// want requires an active turn (m.turnEvents != nil) in addition to
// HasAnimated(). The engine's tool loop can exit on a cancelled context
// without settling the block it was running (see the "Known gap" comment
// where turnClosedMsg resyncs), so a closed turn can leave a block reporting
// ToolRunning forever. Tying want to turnEvents makes the turn's own lifecycle
// — not the block state the engine does not guarantee — the source of truth
// for whether the tick should be running, so a closed turn always disarms
// regardless of what its blocks say.
func (m *Model) syncAnimation() tea.Cmd {
	want := m.turnEvents != nil && m.motionEnabled() && m.list.HasAnimated()
	if want == m.animArmed {
		return nil
	}

	m.animGeneration++
	m.animArmed = want
	if !want {
		// Reset so the next in-flight tool starts from a clean sweep.
		m.animFrame = 0
		m.list.SetFrame(0)
		return nil
	}
	return animTick(m.animGeneration)
}

// advanceAnimation handles one frame of the armed chain and re-arms it. A tick
// from a superseded chain returns no command, ending that chain.
func (m *Model) advanceAnimation(msg animTickMsg) tea.Cmd {
	if !m.animArmed || msg.generation != m.animGeneration {
		return nil
	}
	m.animFrame++
	m.list.SetFrame(m.animFrame)
	return animTick(m.animGeneration)
}

// motionEnabled reports whether the terminal can render the swept chip.
func (m *Model) motionEnabled() bool { return !m.motionDisabled }

// setColorProfile records whether the terminal can render a gradient and
// re-derives the theme accordingly. Unknown means the profile was never
// detected, which is treated as capable rather than incapable so a terminal
// that simply does not answer still animates.
func (m *Model) setColorProfile(profile colorprofile.Profile) tea.Cmd {
	if profile == colorprofile.Unknown {
		return nil
	}
	disabled := profile < minMotionProfile
	if disabled == m.motionDisabled {
		return nil
	}
	m.motionDisabled = disabled
	m.applyStyles(m.theme(m.styles.IsDark))
	m.refreshViewport()
	return m.syncAnimation()
}

// theme builds the terminal theme for the given background, flattening the
// animated chips when motion is disabled. It is the single place both theme
// inputs — background mode and color profile — are combined, so a background
// change cannot resurrect an animation the profile ruled out.
func (m *Model) theme(isDark bool) styles.Theme {
	theme := styles.Default(isDark)
	if m.motionDisabled {
		return theme.WithoutMotion()
	}
	return theme
}

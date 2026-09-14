package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// animInterval is one animation frame. Only in-flight tool items are
// re-rendered per frame, so the cost does not grow with the transcript.
//
// The inspection-group dots advance every eight frames (400ms). The individual
// spinner has the same tick so all live tool activity shares one repaint loop.
const animInterval = 50 * time.Millisecond

// animTickMsg advances tool-activity animation. generation identifies the
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
	want := m.turnEvents != nil && m.list.HasAnimated()
	if want == m.animArmed {
		return nil
	}

	m.animGeneration++
	m.animArmed = want
	if !want {
		// Reset so the next in-flight tool starts from a clean indicator.
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

// theme builds the terminal theme for the given background. Tool motion uses
// glyphs and fixed-width dots, so it remains legible at every color depth.
func (m *Model) theme(isDark bool) styles.Theme {
	return styles.Default(isDark)
}

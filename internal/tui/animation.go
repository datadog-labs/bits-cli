package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

const (
	// animationInterval caps full-model redraws at roughly 30 FPS. Individual
	// animations derive their logical frame from elapsed time, so sharing this
	// repaint clock does not couple their speeds.
	animationInterval = time.Second / 30
	toolAnimInterval  = 50 * time.Millisecond
)

// animationTickMsg drives the one animation clock. generation identifies the
// armed chain, while at lets each animation derive its own logical frame.
type animationTickMsg struct {
	generation uint64
	at         time.Time
}

func animationTick(generation uint64, interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(at time.Time) tea.Msg {
		return animationTickMsg{generation: generation, at: at}
	})
}

// syncAnimations reconciles every animation with current model state. It is
// called after each dispatched message, making mode, size, turn, and approval
// transitions share one lifecycle path.
func (m *Model) syncAnimations() tea.Cmd {
	return m.syncAnimationsAt(time.Now())
}

func (m *Model) syncAnimationsAt(now time.Time) tea.Cmd {
	wantTool := m.toolAnimationWanted()
	wantSweep := m.borderSweepWanted()

	if wantTool != m.toolAnimationActive {
		m.toolAnimationActive = wantTool
		m.animFrame = 0
		m.list.SetFrame(0)
		if wantTool {
			m.toolAnimationStarted = now
		} else {
			m.toolAnimationStarted = time.Time{}
		}
	}

	if wantSweep != m.borderSweepActive {
		m.borderSweepActive = wantSweep
		m.borderSweepFrame = 0
		m.editor.SetSweepFrame(0)
		m.editor.SetWorking(wantSweep)
		if wantSweep {
			m.borderSweepStarted = now
		} else {
			m.borderSweepStarted = time.Time{}
		}
	}

	wantClock := wantTool || wantSweep
	if wantClock == m.animationArmed {
		return nil
	}

	m.animationGeneration++
	m.animationArmed = wantClock
	if !wantClock {
		return nil
	}
	return animationTick(m.animationGeneration, m.animationRepaintInterval())
}

// advanceAnimations samples both logical timelines and re-arms the shared
// repaint clock. A tick from a superseded chain returns no command.
func (m *Model) advanceAnimations(msg animationTickMsg) tea.Cmd {
	if !m.animationArmed || msg.generation != m.animationGeneration {
		return nil
	}

	if m.toolAnimationActive {
		frame := elapsedFrame(msg.at, m.toolAnimationStarted, toolAnimInterval)
		if frame != m.animFrame {
			m.animFrame = frame
			m.list.SetFrame(frame)
		}
	}
	if m.borderSweepActive {
		frame := elapsedFrame(msg.at, m.borderSweepStarted, borderSweepInterval)
		if frame != m.borderSweepFrame {
			m.borderSweepFrame = frame
			m.editor.SetSweepFrame(frame)
		}
	}

	return animationTick(m.animationGeneration, m.animationRepaintInterval())
}

// animationRepaintInterval uses the faster cadence only while the sweep is
// visible. Tool-only activity retains its native 50ms cadence instead of
// paying for redraws whose logical tool frame cannot change.
func (m *Model) animationRepaintInterval() time.Duration {
	if m.borderSweepActive {
		return animationInterval
	}
	return toolAnimInterval
}

func elapsedFrame(now, started time.Time, interval time.Duration) int {
	if now.Before(started) {
		return 0
	}
	return int(now.Sub(started) / interval)
}

// theme builds the terminal theme for the given background. Tool motion uses
// glyphs and fixed-width dots, so it remains legible at every color depth.
func (m *Model) theme(isDark bool) styles.Theme {
	return styles.Default(isDark)
}

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
	animationInterval    = time.Second / 30
	activityAnimInterval = 50 * time.Millisecond
)

// animationTickMsg drives the one animation clock. generation identifies the
// armed chain, while at lets each animation derive its own logical frame.
type animationTickMsg struct {
	generation uint64
	at         time.Time
}

// animationClock owns the lifecycle of the single repaint chain. Sync bumps
// its generation on both edges so a tick left over from an older chain is
// harmless.
type animationClock struct {
	generation uint64
	armed      bool
}

func (c *animationClock) Sync(want bool) bool {
	if want == c.armed {
		return false
	}
	c.generation++
	c.armed = want
	return true
}

// animationTimeline tracks one logical animation independently from the
// shared repaint cadence.
type animationTimeline struct {
	step    time.Duration
	started time.Time
	frame   int
	active  bool
}

func newAnimationTimeline(step time.Duration) animationTimeline {
	return animationTimeline{step: step}
}

func (t *animationTimeline) Sync(want bool, now time.Time) bool {
	if want == t.active {
		return false
	}
	t.active = want
	t.started = time.Time{}
	t.frame = 0
	if want {
		t.started = now
	}
	return true
}

func (t *animationTimeline) Advance(now time.Time) (int, bool) {
	if !t.active || now.Before(t.started) {
		return t.frame, false
	}
	frame := int(now.Sub(t.started) / t.step)
	if frame == t.frame {
		return frame, false
	}
	t.frame = frame
	return frame, true
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
	var wantActivity, wantSweep bool
	switch m.mode {
	case ModeChat:
		wantActivity = m.toolAnimationWanted()
		wantSweep = m.borderSweepWanted()
	case ModeConversations:
		wantActivity = m.picker != nil && m.picker.Animating()
	default:
		// Other screens do not use the shared animation clock.
	}

	if m.animActivity.Sync(wantActivity, now) {
		m.setActivityFrame(0)
	}

	if m.animBorderSweep.Sync(wantSweep, now) {
		m.editor.SetSweepFrame(0)
		m.editor.SetWorking(wantSweep)
	}

	wantClock := wantActivity || wantSweep
	if !m.animClock.Sync(wantClock) {
		return nil
	}
	if !wantClock {
		return nil
	}
	return animationTick(m.animClock.generation, m.animationRepaintInterval())
}

// advanceAnimations samples both logical timelines and re-arms the shared
// repaint clock. A tick from a superseded chain returns no command.
func (m *Model) advanceAnimations(msg animationTickMsg) tea.Cmd {
	if !m.animClock.armed || msg.generation != m.animClock.generation {
		return nil
	}

	if frame, changed := m.animActivity.Advance(msg.at); changed {
		m.setActivityFrame(frame)
	}
	if frame, changed := m.animBorderSweep.Advance(msg.at); changed {
		m.editor.SetSweepFrame(frame)
	}

	return animationTick(m.animClock.generation, m.animationRepaintInterval())
}

// setActivityFrame shares the activity timeline between chat and the picker.
func (m *Model) setActivityFrame(frame int) {
	m.list.SetFrame(frame)
	if m.picker != nil {
		m.picker.SetFrame(frame)
	}
}

// animationRepaintInterval uses the faster cadence only while the sweep is
// visible. Chat and picker activity use the slower 50ms cadence.
func (m *Model) animationRepaintInterval() time.Duration {
	if m.animBorderSweep.active {
		return animationInterval
	}
	return activityAnimInterval
}

// theme builds the terminal theme for the given background. Tool motion uses
// glyphs and fixed-width dots, so it remains legible at every color depth.
func (m *Model) theme(isDark bool) styles.Theme {
	return styles.Default(isDark)
}

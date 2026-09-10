package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// borderSweepInterval is one frame of the composer's border-sweep animation.
// It is a separate clock from animInterval (animation.go): tool-activity
// motion and the composer border sweep are driven by unrelated ticks so
// either can be retimed or disabled independently.
const borderSweepInterval = 40 * time.Millisecond

// borderSweepTickMsg advances the composer border sweep. generation identifies
// the armed tick chain, mirroring animTickMsg's guard against a superseded
// chain double-advancing the frame.
type borderSweepTickMsg struct{ generation uint64 }

// borderSweepTick schedules the next border-sweep frame, stamped with the
// chain it belongs to.
func borderSweepTick(generation uint64) tea.Cmd {
	return tea.Tick(borderSweepInterval, func(time.Time) tea.Msg {
		return borderSweepTickMsg{generation: generation}
	})
}

// syncBorderSweep arms or disarms the border-sweep tick to match m.chatPhase.
// It returns a command only when it starts a chain, so calling it from every
// chatPhase-mutating site is safe and idempotent.
func (m *Model) syncBorderSweep() tea.Cmd {
	want := m.chatPhase == chat.PhaseWaiting || m.chatPhase == chat.PhaseStreaming
	if want == m.borderSweepArmed {
		return nil
	}

	m.borderSweepGeneration++
	m.borderSweepArmed = want
	m.editor.SetWorking(want)
	if !want {
		m.borderSweepFrame = 0
		m.editor.SetSweepFrame(0)
		return nil
	}
	return borderSweepTick(m.borderSweepGeneration)
}

// advanceBorderSweep handles one frame of the armed chain and re-arms it. A
// tick from a superseded chain returns no command, ending that chain.
func (m *Model) advanceBorderSweep(msg borderSweepTickMsg) tea.Cmd {
	if !m.borderSweepArmed || msg.generation != m.borderSweepGeneration {
		return nil
	}
	m.borderSweepFrame++
	m.editor.SetSweepFrame(m.borderSweepFrame)
	return borderSweepTick(m.borderSweepGeneration)
}

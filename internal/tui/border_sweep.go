package tui

import (
	"time"

	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// borderSweepInterval is the composer's logical animation step. The shared
// clock samples this timeline at its lower repaint rate, skipping intermediate
// frames while preserving the sweep's speed.
const borderSweepInterval = 17 * time.Millisecond

func (m *Model) animationsVisible() bool {
	return m.mode == ModeChat && !m.chatViewTooSmall()
}

func (m *Model) toolAnimationWanted() bool {
	return m.animationsVisible() && m.turnEvents != nil && m.list.HasAnimated()
}

// borderSweepWanted distinguishes active work from an approval-only wait. If
// approvals coexist with a running tool, work is still progressing and the
// sweep remains active.
func (m *Model) borderSweepWanted() bool {
	if !m.animationsVisible() || !m.styles.Input.SweepMotion || m.turnEvents == nil {
		return false
	}
	if m.chatPhase != chat.PhaseWaiting && m.chatPhase != chat.PhaseStreaming {
		return false
	}
	return len(m.pendingApprovals) == 0 || m.list.HasAnimated()
}

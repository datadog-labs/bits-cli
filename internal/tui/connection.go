package tui

import (
	"errors"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

// reconnectingDelay is how long reconnecting must last before the transcript
// says so: shorter blips recover on their own and stay silent. Longer ones
// recover on their own too; the row says Bits is still trying.
const reconnectingDelay = 10 * time.Second

// connectionView follows the engine's connection to tell the user about an
// outage once it ends, next to what was last on screen when it began.
type connectionView struct {
	// since is when the outage being followed began; zero while connected.
	since time.Time
	// after is the block that was last when the outage began.
	after agent.BlockID
	// remote is whether another session held prompts back at the last
	// sync: its turn, a stop, or an unanswered tool call.
	remote bool
	// failed is the notice of a send that failed with no response, removed
	// once the conversation shows the message arrived after all; 0 if none.
	failed uint64
}

type reconnectingTickMsg struct{}

// remoteTurn reports whether the conversation is running a turn that this
// session is not driving.
//
// While disconnected the last known turn may be long over, so nothing waits
// on it: a prompt is sent, and the conversation queues it if it must.
func (m *Model) remoteTurn() bool {
	return m.state.Turn == agent.TurnRunning && m.state.Connection.State != agent.ConnectionDisconnected && (!m.op.busy() || m.op.kind != opTurn)
}

// sendBlocked reports whether a prompt must wait for another session: its
// turn, a stop, or a tool call nobody has answered yet.
func (m *Model) sendBlocked() bool {
	return m.remoteTurn() || !m.op.busy() && agent.SendBlocked(m.state)
}

// conversationBusy reports whether the conversation is working on something
// this session is not running: another session's turn, a stop, or a tool
// request that waits for an answer. It shows as a turn running here does.
func (m *Model) conversationBusy() bool {
	if m.op.busy() || m.state.Connection.State == agent.ConnectionDisconnected {
		return false
	}
	return m.state.Turn == agent.TurnRunning || m.state.Stopping || len(m.state.PendingClientCalls()) > 0
}

// connectionStatus is the transcript's live last row, derived from the state.
func (m *Model) connectionStatus(now time.Time) chat.Notice {
	c := m.state.Connection
	switch c.State {
	case agent.ConnectionDisconnected:
		// Only a conversation that refuses to be followed ends the retries.
		return noticeForError("follow the conversation", c.Err)
	case agent.ConnectionReconnecting:
		if m.reconnectingShown(now) {
			if !m.styles.Chat.Motion {
				return notice(chat.NoticeWarn, c.Err, "Reconnecting…")
			}
			return notice(chat.NoticeWarn, c.Err, "Reconnecting%s", strings.Repeat(".", reconnectingDots(m.animActivity.frame)))
		}
	case agent.ConnectionIdle, agent.ConnectionConnecting, agent.ConnectionConnected, agent.ConnectionNew:
	}
	return chat.Notice{}
}

// reconnectingDotStep is how long each of the row's dots lasts.
const reconnectingDotStep = 400 * time.Millisecond

// reconnectingDots is how many dots the row shows at activity frame frame:
// one, two, three, then one again.
func reconnectingDots(frame int) int {
	return 1 + int(time.Duration(frame)*activityAnimInterval/reconnectingDotStep)%3
}

// reconnectingShown reports whether the reconnecting row shows at now.
func (m *Model) reconnectingShown(now time.Time) bool {
	c := m.state.Connection
	return c.State == agent.ConnectionReconnecting && now.Sub(c.Since) >= reconnectingDelay
}

// observeConnection posts what changed in the connection and the remote turn
// since the last sync, and sends the queue once a remote turn ends.
func (m *Model) observeConnection() tea.Cmd {
	var cmds []tea.Cmd
	// Approvals follow the calls the conversation waits on, whichever
	// event last brought the state.
	m.syncApprovals(m.state.PendingApprovals())
	if id := m.conn.failed; id != 0 && !m.state.Unconfirmed {
		m.conn.failed = 0
		m.notices = slices.DeleteFunc(m.notices, func(n chat.NoticeItem) bool { return n.ID == id })
	}
	c := m.state.Connection
	switch c.State {
	case agent.ConnectionReconnecting, agent.ConnectionDisconnected:
		if m.conn.since.IsZero() {
			m.conn.since = c.Since
			if blocks := m.state.Blocks; len(blocks) > 0 {
				m.conn.after = blocks[len(blocks)-1].ID
			}
		}
		if wait := reconnectingDelay - time.Since(c.Since); c.State == agent.ConnectionReconnecting && wait > 0 {
			cmds = append(cmds, tea.Tick(wait, func(time.Time) tea.Msg { return reconnectingTickMsg{} }))
		}
	case agent.ConnectionConnected:
		if !m.conn.since.IsZero() {
			// A drop too brief to show the reconnecting row stays silent.
			if c.Since.Sub(m.conn.since) >= reconnectingDelay {
				cmds = append(cmds, m.postNoticeAfter(m.conn.after, notice(chat.NoticeInfo, nil, "Connection restored.")))
			}
			m.conn.since = time.Time{}
		}
	case agent.ConnectionIdle:
		m.conn.since = time.Time{}
	case agent.ConnectionConnecting, agent.ConnectionNew:
	}
	remote := m.sendBlocked()
	// A prompt whose send failed waits for the user to send it again.
	if m.conn.remote && !remote && !m.op.busy() && len(m.state.Queued) > 0 && !m.state.Unconfirmed {
		cmds = append(cmds, m.startTurn(m.engine.SendQueued, agent.TurnInput{}))
	}
	m.conn.remote = remote
	if !m.op.busy() && m.tools != nil && m.engine != nil && m.engine.CanResumeTools(m.tools) {
		// A call the conversation waits on was approved, as one a stop, a
		// restore or a dropped response left unanswered: run it and answer it.
		cmds = append(cmds, m.resumePendingTools())
	}
	m.syncTranscript()
	return tea.Batch(cmds...)
}

// reconnect tries the conversation again at once if following it failed.
func (m *Model) reconnect() {
	if m.engine != nil {
		m.engine.Reconnect()
	}
}

// repeatsErrorMarker reports whether err only repeats a failure the
// conversation shows: one its message reports, or a generic server error
// after its last block marks the failure.
func repeatsErrorMarker(blocks []agent.Block, err error) bool {
	if _, ok := errors.AsType[*assistant.TurnError](err); ok {
		return true
	}
	if len(blocks) == 0 || !blocks[len(blocks)-1].Error {
		return false
	}
	var apiErr *assistant.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 500
}

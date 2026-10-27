// Package chat renders agent blocks and local UI messages in the transcript.
package chat

import "github.com/datadog-labs/bits-cli/internal/agent"

// Phase is the turn state used by the editor and esc/cancel handling.
type Phase int

const (
	PhaseIdle      Phase = iota
	PhaseLoading         // restoring a conversation's history from the server
	PhaseWaiting         // sent, awaiting first token
	PhaseStreaming       // tokens arriving
	PhaseError
)

// NoticeLevel selects the severity of a local transcript message.
type NoticeLevel int

const (
	NoticeInfo NoticeLevel = iota
	NoticeWarn
	NoticeError
	NoticeFork
	NoticeDone
)

// Notice is a local UI message. Err is retained for diagnostics, not displayed.
type Notice struct {
	Level NoticeLevel
	Text  string
	Err   error
}

// Empty reports whether there is no notice to display.
func (n Notice) Empty() bool { return n.Text == "" }

// NoticeItem positions a session-only UI message right after the block After,
// or before every block when After is zero. It is anchored to a block rather
// than a position, so blocks shown before it, such as a background task's,
// cannot move it. ID gives each local message a stable rendering identity.
type NoticeItem struct {
	ID     uint64
	After  agent.BlockID
	Notice Notice
}

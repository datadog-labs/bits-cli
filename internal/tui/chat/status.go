// Package chat renders agent blocks and local UI messages in the transcript.
package chat

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
)

// Notice is a local UI message. Err is retained for diagnostics, not displayed.
type Notice struct {
	Level NoticeLevel
	Text  string
	Err   error
}

// Empty reports whether there is no notice to display.
func (n Notice) Empty() bool { return n.Text == "" }

// NoticeItem positions a session-only UI message after After agent blocks.
// ID gives each local message a stable rendering identity.
type NoticeItem struct {
	ID     uint64
	After  int
	Notice Notice
}

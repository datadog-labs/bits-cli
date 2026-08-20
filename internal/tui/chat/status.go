// Package chat is the TUI's render layer for the conversation transcript: it
// turns aggregated agent.Block values into styled text (render.go), owns the
// lazily-rendered scrollable view (list.go), and holds the phase/notice UI
// enums. It does not import Bubble Tea and is not safe for concurrent use (the
// tui owns it on the render thread). Conversation aggregation lives in the
// agent package, not here.
package chat

// Phase is the turn state used by the status line and esc/cancel handling.
type Phase int

const (
	PhaseIdle      Phase = iota
	PhaseLoading         // restoring a conversation's history from the server
	PhaseWaiting         // sent, awaiting first token
	PhaseStreaming       // tokens arriving
	PhaseError
)

// NoticeLevel is the severity of a transient status notice shown in the status
// line. It selects the notice's style.
type NoticeLevel int

const (
	NoticeInfo NoticeLevel = iota
	NoticeWarn
	NoticeError
)

// Notice is a transient status message: a severity level and the text shown in
// the bar.
type Notice struct {
	Level NoticeLevel
	Text  string
	Err   error
}

// Empty reports whether there is no notice to display.
func (n Notice) Empty() bool { return n.Text == "" }

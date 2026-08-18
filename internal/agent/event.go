package agent

import "github.com/DataDog/bits-cli/internal/assistant"

// EventKind discriminates the events the engine streams for a turn.
type EventKind int

const (
	EventNone         EventKind = iota // ignored; keeps classify total
	EventDelta                         // text/reasoning fragment for ItemID
	EventTool                          // tool call or result upsert
	EventUsage                         // token accounting
	EventConversation                  // server-assigned/confirmed conversation id
	EventTurnDone                      // the turn completed with no pending tool calls
	EventError                         // the turn failed
)

// ToolCall is the agent-owned tool payload on an event. The tui maps it to a
// transcript view; agent stays independent of the UI-domain chat package.
type ToolCall struct {
	Name   string
	Input  string
	Output string
	Status string // wire status ("success" / "error"); empty for a fresh call
}

// Event is one thing that happened during a turn. It is a plain value carried
// on a channel, with only the fields relevant to Kind populated.
type Event struct {
	Kind    EventKind
	ItemID  string                // stable transcript key (see classify.itemID)
	Role    assistant.Role        // for EventDelta
	Content assistant.ContentKind // for EventDelta: text vs reasoning
	Text    string                // for EventDelta
	Tool    ToolCall              // for EventTool
	Usage   *assistant.Usage      // for EventUsage
	ConvID  string                // for EventConversation
	Err     error                 // for EventError
}

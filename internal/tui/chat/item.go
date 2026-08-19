// Package chat holds the in-memory transcript store for the TUI: the ordered
// list of rendered conversation items plus the phase/permission UI enums. It is
// UI-domain state, patched in place from agent events by the tui layer. It does
// not import Bubble Tea and is not safe for concurrent use (the tui owns it on
// the render thread).
package chat

import "github.com/DataDog/bits-cli/internal/assistant"

// ItemScope namespaces an ItemID so ids drawn from different sources can never
// collide.
type ItemScope int

const (
	ScopeLocal   ItemScope = iota // client-minted: user messages, which have no wire id
	ScopeMessage                  // keyed by Message.MessageID
	ScopeTool                     // keyed by ToolPayload.ToolCallID
)

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

// ToolStatus is the lifecycle state of a tool block. The zero value ToolUnknown
// means "unset", which lets a merge leave an existing status untouched.
type ToolStatus int

const (
	ToolUnknown ToolStatus = iota
	ToolRunning
	ToolSuccess
	ToolError
)

// ToolStatusOf maps a wire tool_response status ("success" / "error", or
// "running") to a ToolStatus. Empty or unrecognized values map to ToolUnknown.
func ToolStatusOf(s string) ToolStatus {
	switch s {
	case "running":
		return ToolRunning
	case "success":
		return ToolSuccess
	case "error":
		return ToolError
	default:
		return ToolUnknown
	}
}

// ToolView is the rendered state of a tool call+result block. A call creates it
// (Status ToolRunning) and the matching result merges into it.
type ToolView struct {
	Name   string
	Input  string
	Output string
	Status ToolStatus
}

// ToolViewOf builds the rendered view of a tool content block. A call and its
// result both map through here and UpsertTool merges them, so fields absent
// from one stay zero rather than overwriting what the other supplied.
// ToolPayload carries more than is rendered today (Title, Detail,
// ArgumentsTruncated); read it here when the renderer wants it.
func ToolViewOf(tp *assistant.ToolPayload) ToolView {
	if tp == nil {
		return ToolView{}
	}
	tv := ToolView{Status: ToolStatusOf(tp.Status)}
	if tp.Metadata != nil {
		tv.Name, tv.Input, tv.Output = tp.Metadata.Name, tp.Metadata.Input, tp.Metadata.Output
	}
	// tool_call_started arrives before any input and names the tool separately.
	if tv.Name == "" {
		tv.Name = tp.ToolName
	}
	return tv
}

// Item is one rendered block in the transcript. Items are keyed by ID for O(1)
// patching; Version is bumped on every mutation and used as a render-memo key
// (with ID + width) so only changed items re-render.
type Item struct {
	ID        ItemID
	Role      assistant.Role
	Kind      assistant.ContentKind
	Text      string
	Tool      ToolView
	Streaming bool
	Collapsed bool // reasoning / long tool output (Ctrl+O later)
	Version   int
}

// ItemID is the stable transcript key for one rendered block. It is comparable,
// so it is used directly as a map key rather than formatted into a string.
type ItemID struct {
	Scope ItemScope
	Key   string
	// Kind splits one message into a block per content kind, so a message that
	// emits reasoning and then an answer yields two items. Unset for ScopeTool,
	// where a call and its result must merge into one block.
	Kind assistant.ContentKind
}

// ItemIDOf derives the transcript key for a streamed message: the shared
// ToolCallID for a tool call and its result, a (MessageID, kind) composite for
// everything else.
func ItemIDOf(msg assistant.Message) ItemID {
	kind := msg.Content.Kind()
	switch kind {
	case assistant.KindToolCall, assistant.KindToolResult:
		// Fall back to the message id when a call has no tool call id, so such
		// calls stay distinct instead of sharing the empty key.
		if msg.Content.Tool != nil && msg.Content.Tool.ToolCallID != "" {
			return ItemID{Scope: ScopeTool, Key: msg.Content.Tool.ToolCallID}
		}
		return ItemID{Scope: ScopeTool, Key: msg.MessageID}
	default:
		return ItemID{Scope: ScopeMessage, Key: msg.MessageID, Kind: kind}
	}
}

// String renders an ItemID for logs, tests, and panic messages.
func (id ItemID) String() string {
	switch id.Scope {
	case ScopeTool:
		return "tool:" + id.Key
	case ScopeMessage:
		return "msg:" + id.Key + "#" + id.Kind.String()
	default:
		return "local:" + id.Key
	}
}

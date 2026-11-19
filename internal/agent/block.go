package agent

import "github.com/datadog-labs/bits-cli/internal/assistant"

// BlockScope namespaces a BlockID so ids drawn from different sources can never
// collide.
type BlockScope int

const (
	ScopeLocal   BlockScope = iota // client-minted rows outside the conversation
	ScopeMessage                   // keyed by Message.MessageID
	ScopeTool                      // keyed by ToolPayload.ToolCallID
	ScopeCopied                    // frozen block from the source conversation, keyed by its original BlockID.String()
	// ScopePrompt keys a user's text prompt by its 1-based place among the
	// conversation's prompts. A prompt's wire id differs between the copy
	// shown live and the saved one, but its place does not, so one prompt
	// keeps one id whether it was typed here, replayed, or loaded.
	ScopePrompt
)

// ToolStatus is the lifecycle state of a tool block.
type ToolStatus int

const (
	ToolUnknown ToolStatus = iota
	ToolRunning
	ToolAwaitingApproval
	ToolSuccess
	ToolError
	ToolCancelled
	ToolDenied
)

// IsTerminal reports whether the call has finished.
func (s ToolStatus) IsTerminal() bool {
	switch s {
	case ToolSuccess, ToolError, ToolCancelled, ToolDenied:
		return true
	case ToolUnknown, ToolRunning, ToolAwaitingApproval:
		return false
	}
	return false
}

// ToolStatusOf maps a wire tool_response status ("success" / "error", or
// "running") to a ToolStatus.
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

// ToolBlock is the aggregated state of a tool call and its result.
type ToolBlock struct {
	Name string
	// Namespace is the wire namespace. Together with Name and IsClientSide it
	// forms the exact identity used by presentation-only tool classification.
	// A nil namespace means the wire event did not provide one; a pointer to an
	// empty string explicitly clears a namespace from an earlier event.
	Namespace *string
	// Input is the authoritative, final tool argument. HasFinalInput indicates
	// that a final tool-call payload supplied it, including an intentionally
	// empty argument.
	Input         string
	HasFinalInput bool
	// InputPartial is the bounded prefix assembled from streamed input deltas.
	// It is kept separate from Input because the latter must remain complete for
	// tool execution while streamed previews must not retain unbounded data.
	InputPartial          string
	InputPreviewTruncated bool
	Output                string
	Status                ToolStatus
	Title                 string
	Detail                string
	// RenderState is opaque, process-local state for a presentation layer. It
	// is never sent to the backend or persisted in history.
	RenderState  any
	IsClientSide bool
	Approval     *ApprovalPrompt
	// Responded records that the conversation holds the call's client tool
	// response, so it must not run again.
	Responded bool
}

// ToolBlockOf builds the aggregated view of a tool content block.
func ToolBlockOf(tp *assistant.ToolPayload) ToolBlock {
	if tp == nil {
		return ToolBlock{}
	}
	tc := ToolBlock{
		Status:       ToolStatusOf(tp.Status),
		Title:        tp.Title,
		IsClientSide: tp.IsClientSide,
	}
	if tp.Metadata != nil {
		tc.Name, tc.Input, tc.Output = tp.Metadata.Name, tp.Metadata.Input, tp.Metadata.Output
		tc.Namespace = tp.Metadata.Namespace
	}
	if tp.Detail != nil {
		tc.Detail = tp.Detail.Content
	}
	// tool_call_started arrives before any input and names the tool separately.
	if tc.Name == "" {
		tc.Name = tp.ToolName
	}
	return tc
}

// Block is one aggregated conversation block: the surface-agnostic result of
// coalescing streamed deltas by id (text/reasoning concatenation, call+result
// merge).
type Block struct {
	ID        BlockID
	Role      assistant.Role
	Kind      assistant.ContentKind
	MessageID string // originating wire message id (empty for client-minted blocks)
	AgentID   string // sub-agent that produced it
	CreatedAt int64
	Complete  bool
	Rev       int
	// Run is the run that last changed the block: 0 if none did, as for
	// restored history. It tells a run's changes apart wherever they sit.
	Run RunID
	// TurnNumber is the number of user messages, prompts and tool answers,
	// up to and including the one whose turn produced the block.
	TurnNumber int
	// Error marks a block that reports the turn failed.
	Error bool

	Markdown  *assistant.MarkdownPayload      // KindText: assistant answer or user prompt
	Thinking  *assistant.ThinkingPayload      // KindReasoning: model thinking
	Tool      *ToolBlock                      // KindToolCall/KindToolResult: merged call+result
	Widget    *assistant.WidgetPayload        // KindWidget
	Dashboard *assistant.DashboardPayload     // KindDashboard
	Progress  *assistant.ProgressPayload      // KindProgress
	Memory    *assistant.MemoryContextPayload // KindMemory

	// Context is the entities a user prompt was sent with, as sent: an image's
	// ID is its whole data URL. The slice and all nested data, including
	// definition values, are shared and must remain immutable.
	Context []assistant.ContextEntity
}

// BlockID is the stable transcript key for one block. It is comparable, so it is
// used directly as a map key rather than formatted into a string.
type BlockID struct {
	Scope BlockScope
	Key   string
	Kind  assistant.ContentKind
}

// Answer returns the assistant's words a block holds: never a failure the
// conversation reports.
func (b Block) Answer() (string, bool) {
	if b.Role != assistant.RoleAssistant || b.Kind != assistant.KindText || b.Error || b.Markdown == nil {
		return "", false
	}
	return b.Markdown.Content, true
}

func (b Block) ToolCallID() string {
	if b.ID.Scope == ScopeTool {
		return b.ID.Key
	}
	return ""
}

// BlockIDOf derives the transcript key for a streamed message: the shared
// ToolCallID for a tool call and its result, a (MessageID, kind) composite for
// everything else.
func BlockIDOf(msg assistant.Message) BlockID {
	kind := msg.Content.Kind()
	switch kind {
	case assistant.KindToolCall, assistant.KindToolResult:
		// Fall back to the message id when a call has no tool call id, so such
		// calls stay distinct instead of sharing the empty key.
		if msg.Content.Tool != nil && msg.Content.Tool.ToolCallID != "" {
			return BlockID{Scope: ScopeTool, Key: msg.Content.Tool.ToolCallID}
		}
		return BlockID{Scope: ScopeTool, Key: msg.MessageID}
	default:
		return BlockID{Scope: ScopeMessage, Key: msg.MessageID, Kind: kind}
	}
}

// String renders a BlockID for logs, tests, and panic messages.
func (id BlockID) String() string {
	switch id.Scope {
	case ScopeCopied:
		return "copied:" + id.Key
	case ScopeTool:
		return "tool:" + id.Key
	case ScopeMessage:
		return "msg:" + id.Key + "#" + id.Kind.String()
	case ScopePrompt:
		return "prompt:" + id.Key
	default:
		return "local:" + id.Key
	}
}

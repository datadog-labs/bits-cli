package agent

import "github.com/DataDog/bits-cli/internal/assistant"

// BlockScope namespaces a BlockID so ids drawn from different sources can never
// collide.
type BlockScope int

const (
	ScopeLocal   BlockScope = iota // client-minted: user messages, which have no wire id
	ScopeMessage                   // keyed by Message.MessageID
	ScopeTool                      // keyed by ToolPayload.ToolCallID
)

// ToolStatus is the lifecycle state of a tool block.
type ToolStatus int

const (
	ToolUnknown ToolStatus = iota
	ToolRunning
	ToolAwaitingApproval
	ToolSuccess
	ToolError
)

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
	// Denied marks an approval refusal, not a tool error.
	Denied bool
	// Cancelled marks a call ended by a user stop rather than a tool failure.
	Cancelled bool
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

	Markdown  *assistant.MarkdownPayload  // KindText: assistant answer or user prompt
	Thinking  *assistant.ThinkingPayload  // KindReasoning: model thinking
	Tool      *ToolBlock                  // KindToolCall/KindToolResult: merged call+result
	Widget    *assistant.WidgetPayload    // KindWidget
	Dashboard *assistant.DashboardPayload // KindDashboard
	Progress  *assistant.ProgressPayload  // KindProgress
}

// BlockID is the stable transcript key for one block. It is comparable, so it is
// used directly as a map key rather than formatted into a string.
type BlockID struct {
	Scope BlockScope
	Key   string
	Kind  assistant.ContentKind
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
	case ScopeTool:
		return "tool:" + id.Key
	case ScopeMessage:
		return "msg:" + id.Key + "#" + id.Kind.String()
	default:
		return "local:" + id.Key
	}
}

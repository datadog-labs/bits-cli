package agent

import (
	"strconv"
	"strings"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// maxToolInputPreviewBytes bounds the raw streamed argument retained in a
// transcript. The final tool-call metadata remains authoritative and is not
// subject to this preview-only limit.
const maxToolInputPreviewBytes = 256 << 10

// Transcript is the ordered set of aggregated conversation blocks with an id
// index for O(1) patching. It is the engine's headless, surface-agnostic view
// of a conversation: it folds streamed wire messages into blocks. Not safe for
// concurrent use; the engine serializes access.
type Transcript struct {
	blocks        []Block
	index         map[BlockID]int // id -> position in blocks
	accumulatorID BlockID
	accumulator   *strings.Builder // pointer-owned: Builder must not be copied after use
	userSeq       int              // monotonic id source for local user messages
	// openStream tracks the currently streaming (incomplete) text/reasoning
	openStream BlockID
	hasOpen    bool
	// inputPreviews are keyed by wire tool-call id because input deltas for
	// multiple calls can be interleaved. Builders are transcript-owned and are
	// never published through a Block snapshot.
	inputPreviews map[string]*toolInputPreview
}

type toolInputPreview struct {
	value     strings.Builder
	truncated bool
}

// NewTranscript returns an empty transcript.
func NewTranscript() *Transcript {
	return &Transcript{
		index:         map[BlockID]int{},
		inputPreviews: map[string]*toolInputPreview{},
	}
}

// Blocks returns the underlying slice. Callers must treat it as read-only.
func (t *Transcript) Blocks() []Block { return t.blocks }

func (t *Transcript) MarkAwaitingApproval(id string, prompt ApprovalPrompt) (Block, bool) {
	p := prompt
	return t.markTool(id, func(tool *ToolBlock) {
		tool.Status = ToolAwaitingApproval
		tool.Approval = &p
	})
}

func (t *Transcript) MarkToolRunning(id string) (Block, bool) {
	return t.markTool(id, func(tool *ToolBlock) {
		tool.Status = ToolRunning
		tool.Approval = nil
	})
}

// SetToolRenderState replaces the opaque local state carried by a tool block.
// markTool copies the ToolBlock before swapping it, preserving snapshots held
// by event consumers.
func (t *Transcript) SetToolRenderState(id string, state any) (Block, bool) {
	return t.markTool(id, func(tool *ToolBlock) {
		tool.RenderState = state
	})
}

func (t *Transcript) MarkToolExecuted(id string, result ToolResult) (Block, bool) {
	return t.markTool(id, func(tool *ToolBlock) {
		tool.Approval = nil
		tool.Status = ToolSuccess
		if result.IsError {
			tool.Status = ToolError
		}
		tool.Denied = result.Denied
		tool.Cancelled = result.Cancelled
		if result.Title != "" {
			tool.Title = result.Title
		}
		if result.Output != "" {
			tool.Output = result.Output
		}
		if result.Display != "" {
			tool.Detail = result.Display
		}
		if result.RenderState != nil {
			tool.RenderState = result.RenderState.State
		}
	})
}

func (t *Transcript) markTool(id string, mutate func(*ToolBlock)) (Block, bool) {
	i, ok := t.index[BlockID{Scope: ScopeTool, Key: id}]
	if !ok || t.blocks[i].Tool == nil {
		return Block{}, false
	}
	tool := *t.blocks[i].Tool
	mutate(&tool)
	t.blocks[i].Tool = &tool
	t.blocks[i].Rev++
	return t.blocks[i], true
}

// AppendUser adds a user message with a locally generated unique id and returns
// the created block. User blocks are complete on creation (never streamed).
func (t *Transcript) AppendUser(text string) Block {
	t.userSeq++
	b := Block{
		ID:       BlockID{Scope: ScopeLocal, Key: strconv.Itoa(t.userSeq)},
		Role:     assistant.RoleUser,
		Kind:     assistant.KindText,
		Markdown: &assistant.MarkdownPayload{Content: text},
		Complete: true,
	}
	t.push(b)
	t.closePrior(b)
	return b
}

// AppendMessage folds a wire message into the transcript and returns the block
// it created or updated, plus whether one was produced.
func (t *Transcript) AppendMessage(msg assistant.Message) (Block, bool) {
	b, ok := t.fold(msg)
	if ok {
		t.closePrior(b)
	}
	return b, ok
}

// fold routes a wire message to the mutator for its kind.
func (t *Transcript) fold(msg assistant.Message) (Block, bool) {
	switch kind := msg.Content.Kind(); kind {
	case assistant.KindText:
		return t.appendMarkdown(msg)
	case assistant.KindReasoning:
		return t.appendReasoning(msg)
	case assistant.KindToolCall, assistant.KindToolResult:
		return t.upsertTool(msg)
	case assistant.KindWidget:
		return t.appendPassthrough(msg, kind, func(b *Block) { b.Widget = msg.Content.Widget })
	case assistant.KindDashboard:
		return t.appendPassthrough(msg, kind, func(b *Block) { b.Dashboard = msg.Content.Dashboard })
	case assistant.KindProgress:
		return t.appendPassthrough(msg, kind, func(b *Block) { b.Progress = msg.Content.Progress })
	case assistant.KindTurnMarker, assistant.KindStop, assistant.KindInternal:
		// Known history-only control metadata is not conversation content. Keep it
		// out of both startup restore and /resume transcripts.
		return Block{}, false
	case assistant.KindUnknown:
		if strings.TrimSpace(msg.Content.Type) == "" {
			// History envelopes can contain metadata-only records with no content
			// discriminator. They are not user-visible conversation content.
			return Block{}, false
		}
		// Future content remains visible as a safe fallback label. Its provider
		// payload is deliberately not rendered.
		return t.appendPassthrough(msg, kind, func(*Block) {})
	}
	return Block{}, false
}

// appendMarkdown appends a streamed answer-text fragment. Fragments sharing an
// id are concatenated into one block; the first creates an incomplete
// (streaming) block capturing the message's context.
func (t *Transcript) appendMarkdown(msg assistant.Message) (Block, bool) {
	p := msg.Content.Markdown
	if p == nil || p.Content == "" {
		return Block{}, false
	}
	id := BlockIDOf(msg)
	if i, ok := t.index[id]; ok {
		acc := t.accumulatorFor(id, t.blocks[i].Markdown.Content)
		_, _ = acc.WriteString(p.Content)
		t.blocks[i].Markdown = &assistant.MarkdownPayload{Content: acc.String()}
		t.blocks[i].Complete = false
		t.blocks[i].Rev++
		return t.blocks[i], true
	}
	acc := t.accumulatorFor(id, "")
	_, _ = acc.WriteString(p.Content)
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      assistant.KindText,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Markdown:  &assistant.MarkdownPayload{Content: acc.String()},
	}
	t.push(b)
	return b, true
}

// appendReasoning appends a streamed thinking fragment. Like answer text the
// body is concatenated across fragments sharing an id, but reasoning keeps its
// own payload so the wire's Redacted flag and provider signature survive the
// fold rather than being flattened into plain text.
func (t *Transcript) appendReasoning(msg assistant.Message) (Block, bool) {
	p := msg.Content.Thinking
	if p == nil || (p.Content == "" && !p.Redacted) {
		return Block{}, false
	}
	id := BlockIDOf(msg)
	if i, ok := t.index[id]; ok {
		prev := t.blocks[i].Thinking
		acc := t.accumulatorFor(id, prev.Content)
		_, _ = acc.WriteString(p.Content)
		next := &assistant.ThinkingPayload{
			Content:          acc.String(),
			EncryptedContent: prev.EncryptedContent,
			Redacted:         prev.Redacted || p.Redacted,
		}
		if p.EncryptedContent != "" {
			next.EncryptedContent = p.EncryptedContent
		}
		t.blocks[i].Thinking = next
		t.blocks[i].Complete = false
		t.blocks[i].Rev++
		return t.blocks[i], true
	}
	acc := t.accumulatorFor(id, "")
	_, _ = acc.WriteString(p.Content)
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      assistant.KindReasoning,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Thinking: &assistant.ThinkingPayload{
			Content:          acc.String(),
			EncryptedContent: p.EncryptedContent,
			Redacted:         p.Redacted,
		},
	}
	t.push(b)
	return b, true
}

// upsertTool creates a tool block on the call and merges the result into it.
func (t *Transcript) upsertTool(msg assistant.Message) (Block, bool) {
	tp := msg.Content.Tool
	if tp == nil {
		return Block{}, false
	}

	// Streamed input events are updates to a call started earlier. They must not
	// manufacture a block for an unknown or missing id: doing so would merge
	// malformed deltas into a shared empty-id block.
	switch msg.Content.Type {
	case assistant.ContentToolCallStarted:
		if tp.ToolCallID == "" {
			return Block{}, false
		}
	case assistant.ContentToolCallInputDelta:
		if tp.ToolCallID == "" {
			return Block{}, false
		}
		if _, ok := t.index[BlockID{Scope: ScopeTool, Key: tp.ToolCallID}]; !ok {
			return Block{}, false
		}
		if _, ok := t.inputPreviews[tp.ToolCallID]; !ok {
			return Block{}, false
		}
	}

	id := BlockIDOf(msg)
	tc := ToolBlockOf(msg.Content.Tool)
	// A client_tool_call is client-side regardless of the wire is_client_side
	// field, which only tool_call_started populates. Deriving here also covers
	// replayed history (restore), which never passes through the engine fold.
	if msg.Content.Type == assistant.ContentClientToolCall {
		tc.IsClientSide = true
	}
	if i, ok := t.index[id]; ok {
		// Merge into a copy of the current aggregate, then swap the pointer, so
		// a snapshot already sharing the old pointer is not mutated.
		merged := *t.blocks[i].Tool
		if tc.Name != "" {
			merged.Name = tc.Name
		}
		if tc.Namespace != nil {
			merged.Namespace = tc.Namespace
		}
		if hasFinalToolInput(msg) {
			merged.Input = tc.Input
			merged.HasFinalInput = true
			merged.InputPartial = ""
			merged.InputPreviewTruncated = false
		}
		if tc.Output != "" {
			merged.Output = tc.Output
		}
		if tc.Title != "" {
			merged.Title = tc.Title
		}
		if tc.Detail != "" {
			merged.Detail = tc.Detail
		}
		if tc.Status != ToolUnknown {
			merged.Status = tc.Status
		}
		merged.IsClientSide = merged.IsClientSide || tc.IsClientSide
		if msg.Content.Type == assistant.ContentToolCallStarted {
			// A repeated started event resets no already-received input; it only
			// refreshes the identifying metadata.
			if preview := t.inputPreviews[tp.ToolCallID]; preview != nil {
				merged.InputPartial = preview.value.String()
				merged.InputPreviewTruncated = preview.truncated
			}
		}
		if msg.Content.Type == assistant.ContentToolCallInputDelta {
			t.inputToolPreview(&merged, tp.ToolCallID, tp.PartialJSON)
		}
		t.blocks[i].Tool = &merged
		t.blocks[i].Rev++
		if hasFinalToolInput(msg) || msg.Content.Type == assistant.ContentToolResponse || msg.Content.Type == assistant.ContentClientToolResponse {
			delete(t.inputPreviews, tp.ToolCallID)
		}
		return t.blocks[i], true
	}

	// Only a started event initializes the per-call preview accumulator. A
	// normal final call can still create a block for backwards compatibility
	// with streams that do not advertise streamed input.
	if msg.Content.Type == assistant.ContentToolCallStarted {
		t.inputPreviews[tp.ToolCallID] = &toolInputPreview{}
	}
	if tc.Status == ToolUnknown {
		tc.Status = ToolRunning
	}
	if hasFinalToolInput(msg) {
		tc.HasFinalInput = true
	}
	if msg.Content.Type == assistant.ContentToolCallStarted {
		t.inputToolPreview(&tc, tp.ToolCallID, "")
	}
	b := Block{
		ID:        id,
		Role:      assistant.RoleAssistant,
		Kind:      assistant.KindToolCall,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Tool:      &tc,
		Complete:  true,
	}
	t.push(b)
	if hasFinalToolInput(msg) {
		delete(t.inputPreviews, tp.ToolCallID)
	}
	return b, true
}

func hasFinalToolInput(msg assistant.Message) bool {
	if msg.Content.Tool == nil || msg.Content.Tool.Metadata == nil {
		return false
	}
	switch msg.Content.Type {
	case assistant.ContentToolCall, assistant.ContentClientToolCall:
		return true
	default:
		return false
	}
}

func (t *Transcript) inputToolPreview(tool *ToolBlock, id, delta string) {
	preview := t.inputPreviews[id]
	if preview == nil {
		preview = &toolInputPreview{}
		t.inputPreviews[id] = preview
	}
	if !preview.truncated && delta != "" {
		remaining := maxToolInputPreviewBytes - preview.value.Len()
		if remaining <= 0 {
			preview.truncated = true
		} else if len(delta) > remaining {
			_, _ = preview.value.WriteString(delta[:remaining])
			preview.truncated = true
		} else {
			_, _ = preview.value.WriteString(delta)
		}
	}
	tool.InputPartial = preview.value.String()
	tool.InputPreviewTruncated = preview.truncated
}

// appendPassthrough upserts a non-streamed block whose payload the block layer
// carries verbatim from the wire (widget, dashboard, progress).
func (t *Transcript) appendPassthrough(msg assistant.Message, kind assistant.ContentKind, set func(*Block)) (Block, bool) {
	id := BlockIDOf(msg)
	if i, ok := t.index[id]; ok {
		set(&t.blocks[i])
		t.blocks[i].Rev++
		return t.blocks[i], true
	}
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      kind,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Complete:  true,
	}
	set(&b)
	t.push(b)
	return b, true
}

// closePrior implements block-scoped completion. It optimistically assumes a
// streamed text/reasoning block is done once a fragment lands on a different
// block id.
//
// Assistant API does not forward thinking start/complete events.
func (t *Transcript) closePrior(b Block) {
	if t.hasOpen && t.openStream != b.ID {
		priorID := t.openStream
		if i, ok := t.index[priorID]; ok && !t.blocks[i].Complete {
			t.blocks[i].Complete = true
			t.blocks[i].Rev++
		}
		t.releaseAccumulator(priorID)
		t.hasOpen = false
	}
	if !b.Complete {
		t.openStream = b.ID
		t.hasOpen = true
	}
}

// FinalizeAll marks every still-open block complete; called at turn end as the
// backstop for the last open block (typically the answer text, which has no
// status glyph). Mid-turn blocks are already closed incrementally by
// closePrior, so this normally only touches that trailing block. It reports
// whether the transcript changed.
func (t *Transcript) FinalizeAll() bool {
	finalized := false
	if t.hasOpen {
		id := t.openStream
		if i, ok := t.index[id]; ok && !t.blocks[i].Complete {
			t.blocks[i].Complete = true
			t.blocks[i].Rev++
			finalized = true
		}
		t.releaseAccumulator(id)
	}
	t.hasOpen = false
	return finalized
}

func (t *Transcript) accumulatorFor(id BlockID, existing string) *strings.Builder {
	if t.accumulator != nil && t.accumulatorID == id {
		return t.accumulator
	}
	acc := &strings.Builder{}
	if existing != "" {
		_, _ = acc.WriteString(existing)
	}
	t.accumulatorID = id
	t.accumulator = acc
	return acc
}

func (t *Transcript) releaseAccumulator(id BlockID) {
	if t.accumulator != nil && t.accumulatorID == id {
		t.accumulatorID = BlockID{}
		t.accumulator = nil
	}
}

func (t *Transcript) push(b Block) {
	// Mutators upsert by id before reaching push, so a duplicate is a programmer
	// error, not bad input; fail loudly.
	if _, ok := t.index[b.ID]; ok {
		panic("agent: push of duplicate block id " + b.ID.String())
	}
	t.index[b.ID] = len(t.blocks)
	t.blocks = append(t.blocks, b)
}

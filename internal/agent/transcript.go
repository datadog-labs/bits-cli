package agent

import (
	"strconv"
	"strings"

	"github.com/DataDog/bits-cli/internal/assistant"
)

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
}

// NewTranscript returns an empty transcript.
func NewTranscript() *Transcript {
	return &Transcript{index: map[BlockID]int{}}
}

// Blocks returns the underlying slice. Callers must treat it as read-only.
func (t *Transcript) Blocks() []Block { return t.blocks }

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
	id := BlockIDOf(msg)
	tc := ToolCallOf(msg.Content.Tool)
	if i, ok := t.index[id]; ok {
		// Merge into a copy of the current aggregate, then swap the pointer, so
		// a snapshot already sharing the old pointer is not mutated.
		merged := *t.blocks[i].Tool
		if tc.Name != "" {
			merged.Name = tc.Name
		}
		if tc.Input != "" {
			merged.Input = tc.Input
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
		t.blocks[i].Tool = &merged
		t.blocks[i].Rev++
		return t.blocks[i], true
	}
	if tc.Status == ToolUnknown {
		tc.Status = ToolRunning
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
	return b, true
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
// closePrior, so this normally only touches that trailing block.
func (t *Transcript) FinalizeAll() {
	if t.hasOpen {
		id := t.openStream
		if i, ok := t.index[id]; ok && !t.blocks[i].Complete {
			t.blocks[i].Complete = true
			t.blocks[i].Rev++
		}
		t.releaseAccumulator(id)
	}
	t.hasOpen = false
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

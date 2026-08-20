package agent

import (
	"strconv"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// Transcript is the ordered set of aggregated conversation blocks with an id
// index for O(1) patching. It is the engine's headless, surface-agnostic view
// of a conversation: it folds streamed wire messages into blocks. Not safe for
// concurrent use; the engine serializes access.
type Transcript struct {
	blocks  []Block
	index   map[BlockID]int // id -> position in blocks
	userSeq int             // monotonic id source for local user messages
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
	return b
}

// AppendMessage folds a wire message into the transcript and returns the block
// it created or updated, plus whether one was produced.
func (t *Transcript) AppendMessage(msg assistant.Message) (Block, bool) {
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
	default:
		// Turn markers, stop, internal, and unknown kinds carry no renderable
		// block yet; the payload is on msg.Content.
		return Block{}, false
	}
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
		t.blocks[i].Markdown = &assistant.MarkdownPayload{Content: t.blocks[i].Markdown.Content + p.Content}
		t.blocks[i].Rev++
		return t.blocks[i], true
	}
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      assistant.KindText,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Markdown:  &assistant.MarkdownPayload{Content: p.Content},
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
		next := &assistant.ThinkingPayload{
			Content:          prev.Content + p.Content,
			EncryptedContent: prev.EncryptedContent,
			Redacted:         prev.Redacted || p.Redacted,
		}
		if p.EncryptedContent != "" {
			next.EncryptedContent = p.EncryptedContent
		}
		t.blocks[i].Thinking = next
		t.blocks[i].Rev++
		return t.blocks[i], true
	}
	b := Block{
		ID:        id,
		Role:      assistant.RoleOf(msg.Role),
		Kind:      assistant.KindReasoning,
		MessageID: msg.MessageID,
		AgentID:   msg.AgentID,
		CreatedAt: msg.CreatedAt,
		Thinking: &assistant.ThinkingPayload{
			Content:          p.Content,
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

// FinalizeAll marks every still-open block complete; called at turn end. The
// rendered output does not depend on Complete, so callers need not re-emit the
// changed blocks.
func (t *Transcript) FinalizeAll() {
	for i := range t.blocks {
		if !t.blocks[i].Complete {
			t.blocks[i].Complete = true
			t.blocks[i].Rev++
		}
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

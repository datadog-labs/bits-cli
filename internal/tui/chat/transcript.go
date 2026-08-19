package chat

import (
	"strconv"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// Transcript is the ordered list of conversation items with an ID index for
// O(1) patching. Not safe for concurrent use.
type Transcript struct {
	items   []Item
	index   map[ItemID]int // ID -> position in items
	userSeq int            // monotonic id source for local user messages
}

// NewTranscript returns an empty transcript.
func NewTranscript() *Transcript {
	return &Transcript{index: map[ItemID]int{}}
}

// Items returns the underlying slice. Callers must treat it as read-only.
func (t *Transcript) Items() []Item { return t.items }

// AppendUser adds a user message with a locally generated unique ID (the wire
// has no id for the message we are about to send).
func (t *Transcript) AppendUser(text string) {
	t.userSeq++
	t.push(Item{
		ID:   ItemID{Scope: ScopeLocal, Key: strconv.Itoa(t.userSeq)},
		Role: assistant.RoleUser,
		Kind: assistant.KindText,
		Text: text,
	})
}

// AppendText appends a streamed text/reasoning fragment. Fragments sharing an
// ID are concatenated into one item; the first creates a streaming item.
//
// An empty delta is a no-op: it must not mint a blank block (the stream's final
// fragment is empty — it exists to carry usage) nor bump Version and force a
// re-render that changes nothing.
func (t *Transcript) AppendText(id ItemID, role assistant.Role, kind assistant.ContentKind, delta string) {
	if delta == "" {
		return
	}
	if i, ok := t.index[id]; ok {
		t.items[i].Text += delta
		t.items[i].Version++
		return
	}
	t.push(Item{ID: id, Role: role, Kind: kind, Text: delta, Streaming: true})
}

// UpsertTool creates a tool block on the call and merges the result into it
// (same ID). The block stays a KindToolCall so one renderer draws call+result
// together; only non-empty result fields overwrite existing ones.
func (t *Transcript) UpsertTool(id ItemID, tv ToolView) {
	if i, ok := t.index[id]; ok {
		cur := &t.items[i].Tool
		if tv.Name != "" {
			cur.Name = tv.Name
		}
		if tv.Input != "" {
			cur.Input = tv.Input
		}
		if tv.Output != "" {
			cur.Output = tv.Output
		}
		if tv.Status != ToolUnknown {
			cur.Status = tv.Status
		}
		t.items[i].Version++
		return
	}
	if tv.Status == ToolUnknown {
		tv.Status = ToolRunning
	}
	t.push(Item{ID: id, Role: assistant.RoleAssistant, Kind: assistant.KindToolCall, Tool: tv})
}

// Fold folds one message — streamed live or replayed from history — into the
// transcript using the same mapping for both paths: text/reasoning fragments
// concatenate by id, a tool call and its result merge into one block. The
// switch is exhaustive over assistant.ContentKind (default = observe-only), so
// a new renderable kind must be handled here to appear in the transcript.
func (t *Transcript) Fold(msg assistant.Message) {
	switch kind := msg.Content.Kind(); kind {
	case assistant.KindText, assistant.KindReasoning:
		// A redacted thinking block has no text and so renders as nothing; showing
		// it needs a renderer for Content.Thinking.Redacted.
		t.AppendText(ItemIDOf(msg), assistant.RoleOf(msg.Role), kind, msg.Content.TextBody())
	case assistant.KindToolCall, assistant.KindToolResult:
		t.UpsertTool(ItemIDOf(msg), ToolViewOf(msg.Content.Tool))
	default:
		// Widget, dashboard, progress, turn markers, stop, internal, and unknown
		// kinds have no renderer yet; the payload is on msg.Content.
	}
}

// FinalizeAll clears the streaming flag on every streaming item; called at turn
// end. It bumps Version on the items it changes so a memoized render drops the
// streaming indicator.
func (t *Transcript) FinalizeAll() {
	for i := range t.items {
		if t.items[i].Streaming {
			t.items[i].Streaming = false
			t.items[i].Version++
		}
	}
}

func (t *Transcript) push(it Item) {
	// Mutators upsert by ID before reaching push, so a duplicate is a
	// programmer error, not bad input; fail loudly.
	if _, ok := t.index[it.ID]; ok {
		panic("chat: push of duplicate item id " + it.ID.String())
	}
	t.index[it.ID] = len(t.items)
	t.items = append(t.items, it)
}

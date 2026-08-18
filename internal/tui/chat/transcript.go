package chat

import (
	"fmt"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// Transcript is the ordered list of conversation items with an ID index for
// O(1) patching. Not safe for concurrent use.
type Transcript struct {
	items   []Item
	index   map[string]int // ID -> position in items
	userSeq int            // monotonic id source for local user messages
}

// NewTranscript returns an empty transcript.
func NewTranscript() *Transcript {
	return &Transcript{index: map[string]int{}}
}

// Items returns the underlying slice. Callers must treat it as read-only.
func (t *Transcript) Items() []Item { return t.items }

// AppendUser adds a user message with a locally generated unique ID (the wire
// has no id for the message we are about to send).
func (t *Transcript) AppendUser(text string) {
	t.userSeq++
	t.push(Item{
		ID:   fmt.Sprintf("user:%d", t.userSeq),
		Role: assistant.RoleUser,
		Kind: assistant.KindText,
		Text: text,
	})
}

// AppendText appends a streamed text/reasoning fragment. Fragments sharing an
// ID are concatenated into one item; the first creates a streaming item.
func (t *Transcript) AppendText(id string, role assistant.Role, kind assistant.ContentKind, delta string) {
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
func (t *Transcript) UpsertTool(id string, tv ToolView) {
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
		panic("chat: push of duplicate item id " + it.ID)
	}
	t.index[it.ID] = len(t.items)
	t.items = append(t.items, it)
}

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

// AppendUser adds a user message with a locally generated unique ID.
func (t *Transcript) AppendUser(text string) {
	t.userSeq++
	t.push(Item{
		ID:   ItemID{Scope: ScopeLocal, Key: strconv.Itoa(t.userSeq)},
		Role: assistant.RoleUser,
		Kind: assistant.KindText,
		Text: text,
	})
}

// appendText appends a streamed text/reasoning fragment. Fragments sharing an
// ID are concatenated into one item; the first creates a streaming item.
//
// An empty delta is a no-op: it must not mint a blank block (the stream's final
// fragment is empty — it exists to carry usage) nor bump Version and force a
// re-render that changes nothing.
func (t *Transcript) appendText(id ItemID, role assistant.Role, kind assistant.ContentKind, delta string) {
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

// upsertTool creates a tool block on the call and merges the result into it
// (same ID). The block stays a KindToolCall so one renderer draws call+result
// together; only non-empty result fields overwrite existing ones.
func (t *Transcript) upsertTool(id ItemID, tv ToolView) {
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

// AppendMessage adds a backend message to the transcript.
func (t *Transcript) AppendMessage(msg assistant.Message) {
	switch kind := msg.Content.Kind(); kind {
	case assistant.KindText, assistant.KindReasoning:
		t.appendText(ItemIDOf(msg), assistant.RoleOf(msg.Role), kind, msg.Content.TextBody())
	case assistant.KindToolCall, assistant.KindToolResult:
		t.upsertTool(ItemIDOf(msg), ToolViewOf(msg.Content.Tool))
	default:
		// Widget, dashboard, progress, turn markers, stop, internal, and unknown
		// kinds have no renderer yet; the payload is on msg.Content.
	}
}

// FinalizeAll clears the streaming flag on every streaming item; called at turn
// end.
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

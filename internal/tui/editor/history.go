package editor

import tea "charm.land/bubbletea/v2"

// history is the prompt-recall state: Up from an empty prompt loads the newest
// entry and walks older ones; Down walks back and past the newest empties the
// prompt again. It is active while entries is non-nil.
type history struct {
	// source returns the entries, oldest first. It is read once on entry, so a
	// transcript that changes while browsing does not shift the position.
	source   func() []string
	entries  []string
	index    int
	recalled string // the loaded text; any divergence ends history mode
}

func (h *history) active() bool { return h.entries != nil }

func (h *history) end() { h.entries, h.index, h.recalled = nil, 0, "" }

// SetHistorySource sets the prompts Up can recall, oldest first. The editor
// stays chat-agnostic: the parent decides what counts as a previous prompt.
// The editor keeps the returned slice while browsing, so source must return
// one it no longer mutates.
func (e *Editor) SetHistorySource(source func() []string) { e.history.source = source }

// historyKey handles Up/Down for prompt recall and reports whether it consumed
// the key. History mode is entered only from an empty prompt; while it is
// active, every Up/Down switches entry regardless of the entry's line count.
func (e *Editor) historyKey(key string) (tea.Cmd, bool) {
	h := &e.history
	switch key {
	case "up", "ctrl+p":
		if h.active() {
			if h.index == 0 {
				return nil, true
			}
			h.index--
			return e.recall(h.entries[h.index]), true
		}
		if e.ta.Value() != "" || h.source == nil {
			return nil, false
		}
		entries := h.source()
		if len(entries) == 0 {
			return nil, false
		}
		h.entries, h.index = entries, len(entries)-1
		return e.recall(h.entries[h.index]), true
	case "down", "ctrl+n":
		if !h.active() {
			return nil, false
		}
		if h.index < len(h.entries)-1 {
			h.index++
			return e.recall(h.entries[h.index]), true
		}
		e.Reset()
		return nil, true
	}
	return nil, false
}

// recall loads text as a fresh prompt with the cursor at its end. The value is
// marked dismissed so a trailing @mention or leading /command is not treated
// as a completion trigger.
func (e *Editor) recall(text string) tea.Cmd {
	e.revision++
	e.ta.SetValue(text)
	// SetValue neither refreshes the textarea's viewport content nor scrolls
	// it, so a recall taller than the visible rows would show its head. An
	// empty update refreshes the content and height and scrolls to the cursor,
	// as after any edit.
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(nil)
	// The textarea sanitizes input (tabs, CR, control characters) and caps
	// its content, so the value may differ from text: track what it holds.
	value := e.ta.Value()
	e.attachments = nil
	e.completedMentions = nil
	e.dismissedValue = value
	e.closeMenu()
	e.invalidateBody()
	e.history.recalled = value
	return cmd
}

// syncHistory leaves history mode once the prompt is no longer the recalled
// text with the cursor at its end: an edit or a cursor move hands Up/Down back
// to the textarea.
func (e *Editor) syncHistory() {
	if !e.history.active() {
		return
	}
	value := e.ta.Value()
	if value != e.history.recalled || e.cursorOffset() != len([]rune(value)) {
		e.history.end()
	}
}

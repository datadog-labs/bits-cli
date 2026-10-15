package editor

import (
	"slices"
)

// promptQueue owns unsent prompts. Its lifetime is the conversation, independent
// of draft resets and individual engine operations. Only the parent dispatches.
type promptQueue struct {
	items  []Prompt
	paused bool
}

// Enqueue appends a snapshot. Duplicate prompts are intentional and retained.
func (e *Editor) Enqueue(p Prompt) {
	e.queue.items = append(e.queue.items, p.clone())
}

func (e *Editor) QueueLen() int     { return len(e.queue.items) }
func (e *Editor) QueuePaused() bool { return e.queue.paused }
func (e *Editor) PauseQueue()       { e.queue.paused = len(e.queue.items) > 0 }
func (e *Editor) ResumeQueue()      { e.queue.paused = false }

// QueuedPrompts returns an independent snapshot in submission order. The parent
// chooses the batch boundary; the editor keeps each entry independently removable.
func (e *Editor) QueuedPrompts() []Prompt {
	prompts := make([]Prompt, len(e.queue.items))
	for i, p := range e.queue.items {
		prompts[i] = p.clone()
	}
	return prompts
}

// ConsumeQueued removes a dispatched prefix without disturbing the draft.
func (e *Editor) ConsumeQueued(count int) {
	e.removeQueued(0, min(max(0, count), len(e.queue.items)))
}

// RemoveQueued removes one unsent prompt. Indexes refer to the current queue;
// all mutations and input handling happen synchronously on the UI thread.
func (e *Editor) RemoveQueued(index int) {
	if index < 0 || index >= len(e.queue.items) {
		return
	}
	e.removeQueued(index, index+1)
}

func (e *Editor) removeQueued(start, end int) {
	if start == end {
		return
	}
	e.queue.items = slices.Delete(e.queue.items, start, end)
	if len(e.queue.items) == 0 {
		e.queue.paused = false
	}
}

func (e *Editor) ClearQueue() { e.queue = promptQueue{} }

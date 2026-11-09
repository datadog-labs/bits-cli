package editor

import (
	"image"
	"slices"
)

// QueuedPrompt is an unsent prompt. The parent owns the queue; the editor
// mirrors it for review and removal.
type QueuedPrompt struct {
	ID uint64
	HistoryEntry
}

type promptQueue struct {
	items  []QueuedPrompt
	paused bool
	remove func(id uint64)
}

// SetQueue mirrors the parent's queue in send order. Paused means it waits
// for the user to resume it.
func (e *Editor) SetQueue(items []QueuedPrompt, paused bool) {
	same := slices.EqualFunc(items, e.queue.items, func(a, b QueuedPrompt) bool { return a.ID == b.ID })
	var selected uint64
	if e.queueUI.cursor < len(e.queue.items) {
		selected = e.queue.items[e.queueUI.cursor].ID
	}
	e.queue.items, e.queue.paused = items, paused && len(items) > 0
	if same {
		return
	}
	// The selection follows its prompt; a removed one passes to the next.
	if i := slices.IndexFunc(items, func(q QueuedPrompt) bool { return q.ID == selected }); i >= 0 {
		e.queueUI.cursor = i
	}
	e.queueUI.cursor = min(e.queueUI.cursor, max(0, len(items)-1))
	e.queueUI.pointer = image.Pt(-1, -1)
	e.queueUI.hits = nil
	if len(items) == 0 && e.focus == focusQueue {
		e.focus = focusText
		e.invalidateBody()
	}
}

// SetQueueRemover receives the prompts the user removes from the queue.
func (e *Editor) SetQueueRemover(remove func(id uint64)) { e.queue.remove = remove }

func (e *Editor) QueueLen() int     { return len(e.queue.items) }
func (e *Editor) QueuePaused() bool { return e.queue.paused }

// RemoveQueued removes the prompt at index and reports it to the parent.
func (e *Editor) RemoveQueued(index int) {
	if index < 0 || index >= len(e.queue.items) {
		return
	}
	id := e.queue.items[index].ID
	e.SetQueue(slices.Delete(slices.Clone(e.queue.items), index, index+1), e.queue.paused)
	if e.queue.remove != nil {
		e.queue.remove(id)
	}
}

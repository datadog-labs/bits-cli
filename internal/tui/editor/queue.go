package editor

import (
	"image"
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
	e.queueUI.pointer = image.Pt(-1, -1)
}

func (e *Editor) QueueLen() int     { return len(e.queue.items) }
func (e *Editor) QueuePaused() bool { return e.queue.paused }
func (e *Editor) PauseQueue()       { e.queue.paused = len(e.queue.items) > 0 }
func (e *Editor) ResumeQueue()      { e.queue.paused = false }

// QueuedPrompts copies prompts and attachment slices in submission order. The parent
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
	switch {
	case e.queueUI.cursor >= end:
		e.queueUI.cursor -= end - start
	case e.queueUI.cursor >= start:
		e.queueUI.cursor = start
	}
	e.queueUI.cursor = min(e.queueUI.cursor, max(0, len(e.queue.items)-1))
	e.queueUI.pointer = image.Pt(-1, -1)
	e.queueUI.hits = nil
	if len(e.queue.items) == 0 {
		e.queue.paused = false
		if e.focus == focusQueue {
			e.focus = focusText
			e.invalidateBody()
		}
	}
}

func (e *Editor) ClearQueue() {
	e.queue = promptQueue{}
	e.queueUI = queuePresentation{pointer: image.Pt(-1, -1)}
	if e.focus == focusQueue {
		e.focus = focusText
		e.invalidateBody()
	}
}

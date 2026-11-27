package editor

import (
	"image"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/textarea"
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
	// mentions are the accepted mentions of queued prompts, by ID, in case
	// they return to the composer.
	mentions map[uint64][]trackedMention
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
	for id := range e.queue.mentions {
		if !slices.ContainsFunc(items, func(q QueuedPrompt) bool { return q.ID == id }) {
			delete(e.queue.mentions, id)
		}
	}
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

// RememberQueuedMentions keeps the draft's accepted mentions for the queued
// prompt id, whose text is the draft as sent.
func (e *Editor) RememberQueuedMentions(id uint64, text string) {
	raw := e.ta.Value()
	// The text sent is the draft trimmed; a text that differs otherwise, as a
	// resolved skill does, has no offsets to keep.
	left := strings.TrimLeftFunc(raw, unicode.IsSpace)
	if strings.TrimRightFunc(left, unicode.IsSpace) != text {
		return
	}
	trimmed := utf8.RuneCountInString(raw) - utf8.RuneCountInString(left)
	runes := []rune(text)
	var mentions []trackedMention
	for _, mention := range e.completedMentions {
		if mention.applyEdit(0, trimmed, 0, runes) {
			mentions = append(mentions, mention)
		}
	}
	if len(mentions) == 0 {
		return
	}
	if e.queue.mentions == nil {
		e.queue.mentions = make(map[uint64][]trackedMention)
	}
	e.queue.mentions[id] = mentions
}

// ComposeQueued returns the draft that puts queued in front of the current
// one: their texts, then the draft's as typed, with the draft's caret and all
// accepted mentions. Attachments are merged by identity, and the draft's copy
// wins. It reports false if the composer would not hold the text unchanged.
func (e *Editor) ComposeQueued(queued []QueuedPrompt) (HistoryEntry, bool) {
	var entry HistoryEntry
	var text strings.Builder
	offset, row := 0, 0 // the end of text, in runes and logical rows
	// appendText adds part as a paragraph, with its mentions, and returns the
	// row it starts on.
	appendText := func(part string, mentions []trackedMention) int {
		if text.Len() > 0 {
			text.WriteString("\n\n")
			offset, row = offset+2, row+2
		}
		for _, mention := range mentions {
			mention.start += offset
			mention.end += offset
			entry.mentions = append(entry.mentions, mention)
		}
		start := row
		text.WriteString(part)
		offset += utf8.RuneCountInString(part)
		row += strings.Count(part, "\n")
		return start
	}
	for _, prompt := range queued {
		if strings.TrimSpace(prompt.Text) != "" {
			appendText(prompt.Text, e.queue.mentions[prompt.ID])
		}
		for _, attachment := range prompt.Attachments {
			if !slices.ContainsFunc(entry.Attachments, attachment.sameItem) {
				entry.Attachments = append(entry.Attachments, attachment)
			}
		}
	}
	for _, attachment := range e.attachments {
		if i := slices.IndexFunc(entry.Attachments, attachment.sameItem); i >= 0 {
			entry.Attachments[i] = attachment
		} else {
			entry.Attachments = append(entry.Attachments, attachment)
		}
	}
	if draft := e.ta.Value(); draft != "" {
		start := appendText(draft, e.completedMentions)
		entry.caret = &textarea.Position{Row: start + e.ta.Line(), Col: e.ta.Column()}
	}
	entry.Text = text.String()
	return entry, e.fits(entry.Text)
}

// fits reports whether the composer would hold text unchanged at its current
// width, without touching the live textarea.
func (e *Editor) fits(text string) bool {
	// Each logical line takes at least one row, and the textarea is slow to
	// trim input far over its row limit.
	if strings.Count(text, "\n") >= e.ta.MaxContentHeight {
		return false
	}
	probe := textarea.New()
	probe.ShowLineNumbers = false
	probe.Prompt = ""
	probe.CharLimit = e.ta.CharLimit
	probe.MaxHeight = e.ta.MaxHeight
	probe.MaxContentHeight = e.ta.MaxContentHeight
	probe.SetWidth(e.ta.Width())
	probe.SetValue(text)
	return probe.Value() == text
}

// SetQueueRemover receives the prompts the user removes from the queue.
func (e *Editor) SetQueueRemover(remove func(id uint64)) { e.queue.remove = remove }

func (e *Editor) QueueLen() int     { return len(e.queue.items) }
func (e *Editor) QueuePaused() bool { return e.queue.paused }

// QueueAnimating reports whether pending prompts pulse; a paused queue and
// reduced-motion themes keep a static marker.
func (e *Editor) QueueAnimating() bool {
	return e.inputStyle.SweepMotion && e.QueueLen() > 0 && !e.QueuePaused()
}

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

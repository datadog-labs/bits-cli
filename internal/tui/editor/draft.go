package editor

import "slices"

// Draft retains submission text and tracked mention spans without exposing
// editor internals to the turn controller.
type Draft struct {
	value           string
	attachments     []trackedAttachment
	mentions        []trackedMention
	restoreRevision uint64
}

// TakeDraft clears the composer and returns a recoverable submission.
func (e *Editor) TakeDraft() Draft {
	draft := Draft{value: e.Value(), attachments: slices.Clone(e.attachments), mentions: slices.Clone(e.completedMentions)}
	e.Reset()
	draft.restoreRevision = e.revision
	return draft
}

// RestoreDraft recovers a rejected submission only if the composer has not been
// edited since it was taken, even if later edits left the composer empty again.
func (e *Editor) RestoreDraft(draft Draft) bool {
	if e.revision != draft.restoreRevision {
		return false
	}
	e.Reset()
	e.ta.SetValue(draft.value)
	e.attachments = slices.Clone(draft.attachments)
	e.completedMentions = slices.Clone(draft.mentions)
	e.dismissedValue = e.Value()
	e.ta, _ = e.ta.Update(nil)
	e.invalidateBody()
	return true
}

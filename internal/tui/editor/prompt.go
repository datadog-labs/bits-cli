package editor

import "slices"

// Prompt is a draft, queued message, or recalled message with its attachments.
// It contains editor data only; the parent supplies execution policy at dispatch.
type Prompt struct {
	Text        string
	Attachments []Attachment
}

func (p Prompt) clone() Prompt {
	p.Attachments = slices.Clone(p.Attachments)
	return p
}

// Snapshot copies the current draft and its attachment slice.
func (e *Editor) Snapshot() Prompt {
	return (Prompt{Text: e.Value(), Attachments: e.attachments}).clone()
}

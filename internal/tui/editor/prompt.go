package editor

import (
	"maps"
	"slices"
)

// Prompt is a draft, queued message, or recalled message with its attachments.
// It contains editor data only; the parent supplies execution policy at dispatch.
type Prompt struct {
	Text        string
	Attachments []Attachment
}

func (p Prompt) clone() Prompt {
	p.Attachments = slices.Clone(p.Attachments)
	for i := range p.Attachments {
		a := &p.Attachments[i]
		a.TrackingAttributes = maps.Clone(a.TrackingAttributes)
		if a.RankScore != nil {
			score := *a.RankScore
			a.RankScore = &score
		}
	}
	return p
}

// Snapshot returns an independent copy of the current draft.
func (e *Editor) Snapshot() Prompt {
	return (Prompt{Text: e.Value(), Attachments: e.attachments}).clone()
}

package agent

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

// ErrNothingQueued is returned by SendQueued when the queue is empty.
var ErrNothingQueued = errors.New("no queued prompts")

// QueueID identifies a queued prompt for the engine's lifetime.
type QueueID uint64

// QueuedPrompt is a TurnInput's message waiting for the running turn to end.
type QueuedPrompt struct {
	ID             QueueID
	Message        string
	Skill          *SkillInvocation
	Context        *assistant.AssistantContext
	SkillOverrides []assistant.SkillOverride
}

// Queue adds in's prompt to the queue. A running turn sends the queue, in
// the same run, once it completes. Otherwise the queue waits for SendQueued:
// it is left by a turn that failed, was cancelled, or stopped (StopTools or
// a DenyStop denial), or queued after the turn ended. Queued prompts run
// with the tools, policy and context of the run that sends them. Queue
// fails with ErrOperationActive while an operation other than a turn runs.
func (e *Engine) Queue(in TurnInput) error {
	err := ErrClosed
	e.do(func() {
		if err = ErrOperationActive; e.busy && e.turnRun == 0 {
			return
		}
		// Nothing queued could be sent, and SendQueued would drop it.
		if err = ErrReadOnlyConversation; e.readOnly {
			return
		}
		e.lastQueued++
		e.queue = append(e.queue, QueuedPrompt{ID: e.lastQueued, Message: in.Message, Skill: in.Skill, Context: in.Context, SkillOverrides: in.SkillOverrides})
		e.emit(e.turnRun, Event{Kind: EventState, State: e.state()})
		err = nil
	})
	return err
}

// SendQueued starts a turn with the prompts the queue sends first. Only in's
// tools and policy are used.
func (e *Engine) SendQueued(ctx context.Context, in TurnInput) (RunID, error) {
	var run RunID
	err := e.idle(func() (err error) {
		if e.life.Err() != nil {
			return ErrClosed
		}
		in, ok := e.dequeue(in)
		if !ok {
			return ErrNothingQueued
		}
		run, err = e.launch(ctx, in, nil)
		return err
	})
	return run, err
}

// Unqueue removes prompts not yet sent, all or none. It fails with ErrStale
// once any of them is removed or taken for a request, which may precede the
// state that shows it sent.
func (e *Engine) Unqueue(ids ...QueueID) error {
	err := ErrClosed
	e.do(func() {
		kept := slices.DeleteFunc(slices.Clone(e.queue), func(q QueuedPrompt) bool { return slices.Contains(ids, q.ID) })
		if err = ErrStale; len(e.queue)-len(kept) != len(ids) {
			return
		}
		e.queue = kept
		e.emit(e.turnRun, Event{Kind: EventState, State: e.state()})
		err = nil
	})
	return err
}

// dequeue removes the prompts the next request sends and returns in with
// their message.
func (e *Engine) dequeue(in TurnInput) (TurnInput, bool) {
	batch, n := BatchQueued(e.queue)
	if n == 0 {
		return in, false
	}
	in.Message, in.Skill, in.Context, in.SkillOverrides = batch.Message, batch.Skill, batch.Context, batch.SkillOverrides
	e.queue = slices.Delete(e.queue, 0, n)
	return in, true
}

// BatchQueued returns the first request prompts would send, and how many of
// them it carries: the prompts up to the first skill invocation, which is sent
// alone because a turn carries one skill, and up to MaxImages images. A prompt
// with skill overrides is sent alone so they apply to it only.
func BatchQueued(prompts []QueuedPrompt) (TurnInput, int) {
	if len(prompts) == 0 {
		return TurnInput{}, 0
	}
	first := prompts[0]
	in := TurnInput{Message: first.Message, Skill: first.Skill, Context: first.Context, SkillOverrides: first.SkillOverrides}
	if sentAlone(first) || len(prompts) == 1 {
		return in, 1
	}
	type identity struct {
		kind assistant.EntityType
		id   string
	}
	var texts []string
	var merged assistant.AssistantContext
	seen := make(map[identity]bool)
	images, n := 0, 0
	for _, q := range prompts {
		if sentAlone(q) {
			break
		}
		// Marking entities seen before the limit check is safe: a prompt
		// over the limit ends the batch.
		var added []assistant.ContextEntity
		addedImages := 0
		if q.Context != nil {
			for _, entity := range q.Context.Entities {
				if key := (identity{entity.Type, entity.ID}); !seen[key] {
					seen[key] = true
					added = append(added, entity)
					if isImage(entity) {
						addedImages++
					}
				}
			}
		}
		// Keep a prompt whole: one whose images would exceed the limit
		// waits for the next request.
		if n > 0 && images+addedImages > imageinput.MaxImages {
			break
		}
		n++
		images += addedImages
		merged.Entities = append(merged.Entities, added...)
		if q.Context != nil {
			merged.Resources = append(merged.Resources, q.Context.Resources...)
		}
		if text := strings.TrimSpace(q.Message); text != "" {
			texts = append(texts, q.Message)
		}
	}
	if text := strings.Join(texts, "\n\n"); text != "" {
		in.Message = text // else an attachment-only message keeps its placeholder
	}
	in.Context = nil
	if len(merged.Entities) > 0 || len(merged.Resources) > 0 {
		in.Context = &merged
	}
	return in, n
}

func sentAlone(q QueuedPrompt) bool { return q.Skill != nil || len(q.SkillOverrides) > 0 }

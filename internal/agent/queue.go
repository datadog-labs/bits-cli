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

// ErrSendBlocked is returned by StartTurn and SendQueued while a prompt must
// wait; StartTurn queues its prompt.
var ErrSendBlocked = errors.New("the prompt waits for the running turn and unanswered tool calls")

// PendingClientCalls lists the client tool calls the conversation shows with
// no answer, on the wire or local, whichever session made them.
func (s State) PendingClientCalls() []string {
	var ids []string
	for _, b := range s.Blocks {
		if tool := b.Tool; tool != nil && tool.IsClientSide && !tool.Responded && !tool.Status.IsTerminal() {
			ids = append(ids, b.ToolCallID())
		}
	}
	return ids
}

// SendBlocked reports whether a user prompt must wait: a turn runs, here or
// elsewhere (a local run also holds the engine itself), a stop is pending, or a client tool call has no answer yet. A
// prompt sent then could run ahead of that answer, and its turn's calls
// would reach whoever sent the answer. A round's own answers never wait,
// since they answer the calls that block.
//
// NOTE: two clients sending at the same instant can still cross; a full
// guarantee needs the service to name the message each event answers.
func SendBlocked(s State) bool {
	return !current(s) || s.Turn == TurnRunning || s.Stopping || len(s.PendingClientCalls()) > 0
}

// current reports whether s shows the conversation as it is now. Until a
// replay after the last (re)connect arrives, a turn another session started
// meanwhile, and its tool calls, are unknown, so a prompt waits for it.
func current(s State) bool {
	switch s.Connection.State {
	case ConnectionConnecting, ConnectionReconnecting, ConnectionDisconnected:
		return s.ConversationID == ""
	case ConnectionIdle, ConnectionConnected, ConnectionNew:
		// A conversation not followed, or not created yet, has nothing
		// another session can be running.
	}
	return true
}

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
		e.reconnect()
		e.lastQueued++
		e.queue = append(e.queue, QueuedPrompt{ID: e.lastQueued, Message: in.Message, Skill: in.Skill, Context: in.Context, SkillOverrides: in.SkillOverrides})
		e.emit(e.turnRun, Event{Kind: EventState, State: e.state()})
		err = nil
	})
	return err
}

// Retry has a prompt whose send failed wait like any other queued prompt,
// to be sent once nothing holds it, as when the user asks to send it again
// while the conversation is busy or out of reach.
func (e *Engine) Retry() {
	e.do(func() {
		// Like a new message, an explicit retry tries the conversation at
		// once rather than waiting out the backoff.
		e.reconnect()
		if e.failed != nil {
			e.failed = nil
			e.publish()
		}
	})
}

// SendQueued starts a turn with the prompts the queue sends first. Only in's
// tools and policy are used.
func (e *Engine) SendQueued(ctx context.Context, in TurnInput) (RunID, error) {
	var run RunID
	err := e.idle(func() (err error) {
		if e.life.Err() != nil {
			return ErrClosed
		}
		if len(e.queue) > 0 && SendBlocked(e.state()) {
			return ErrSendBlocked
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

// Unqueue removes a prompt not yet sent. It fails with ErrStale once the
// prompt is removed or taken for a request, which may precede the state that
// shows it sent.
func (e *Engine) Unqueue(id QueueID) error {
	err := ErrClosed
	e.do(func() {
		i := slices.IndexFunc(e.queue, func(q QueuedPrompt) bool { return q.ID == id })
		if err = ErrStale; i < 0 {
			return
		}
		e.queue = slices.Delete(e.queue, i, i+1)
		e.emit(e.turnRun, Event{Kind: EventState, State: e.state()})
		err = nil
	})
	return err
}

// dequeue removes the prompts the next request sends and returns in with
// their message: the queued prompts up to the first skill invocation, which
// is sent alone because a turn carries one skill, and up to MaxImages images.
// A prompt with skill overrides is sent alone so they apply to it only.
func (e *Engine) dequeue(in TurnInput) (TurnInput, bool) {
	if len(e.queue) == 0 {
		return in, false
	}
	first := e.queue[0]
	in.Message, in.Skill, in.Context, in.SkillOverrides = first.Message, first.Skill, first.Context, first.SkillOverrides
	if sentAlone(first) || len(e.queue) == 1 {
		e.queue = slices.Delete(e.queue, 0, 1)
		return in, true
	}
	type identity struct {
		kind assistant.EntityType
		id   string
	}
	var texts []string
	var merged assistant.AssistantContext
	seen := make(map[identity]bool)
	images, n := 0, 0
	for _, q := range e.queue {
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
	e.queue = slices.Delete(e.queue, 0, n)
	return in, true
}

func sentAlone(q QueuedPrompt) bool { return q.Skill != nil || len(q.SkillOverrides) > 0 }

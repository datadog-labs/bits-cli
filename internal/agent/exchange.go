package agent

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// DuplexBackend keeps the response stream open while independently accepting
// completed local tools. Callbacks carry execution-ready calls with unique IDs;
// Stream returns only on a terminal event, cancellation, or transport failure.
type DuplexBackend interface {
	Stream(context.Context, string, assistant.SendOptions, func(assistant.AssistantResponse) error) error
	SubmitToolResults(context.Context, []assistant.ClientToolResponse) error
}

// exchange handles one turn's traffic with the backend. run emits events in
// order and returns the last conversation ID and terminal error. emit returns
// false when the turn stops listening. answer submits an executed call's result
// without blocking; it is called only after run emits that call. final reports
// whether ending the exchange makes every streamed tool terminal.
type exchange interface {
	run(ctx context.Context, emit func(exchangeEvent) bool) (conversation string, err error)
	answer(assistant.ClientToolResponse)
	final() bool
}

// exchangeEvent is one backend fact, applied field by field. Only execute
// makes a client call run: content alone, whatever its type, never does.
type exchangeEvent struct {
	round        int    // starts this later backend send; turns start at 1
	conversation string // server-assigned conversation ID
	message      *assistant.Message
	execute      []ToolCall // a batch, dispatched at once
}

// backendError marks a failure at the Assistant backend boundary.
type backendError struct{ error }

func (e backendError) Unwrap() error { return e.error }

// roundsExchange sends one request per round: the user message, then the
// answers to each round's client calls, which the backend pauses on.
type roundsExchange struct {
	backend Backend
	message any
	opts    assistant.SendOptions
	resumed []ToolCall // answered in the first round, instead of sending message

	mu       sync.Mutex
	answered []assistant.ClientToolResponse
	ready    chan struct{}
}

func newRoundsExchange(backend Backend, message any, opts assistant.SendOptions, resumed []ToolCall) *roundsExchange {
	return &roundsExchange{backend: backend, message: message, opts: opts, resumed: resumed, ready: make(chan struct{}, 1)}
}

func (x *roundsExchange) run(ctx context.Context, emit func(exchangeEvent) bool) (string, error) {
	message, opts, calls := x.message, x.opts, x.resumed
	for round := 1; round <= maxTurns; round++ {
		if round > 1 && !emit(exchangeEvent{round: round}) {
			return opts.ConversationID, ctx.Err()
		}
		if calls == nil {
			var err error
			if calls, err = x.send(ctx, message, &opts, emit); err != nil || len(calls) == 0 {
				return opts.ConversationID, err
			}
		}
		answers, err := x.collect(ctx, calls, emit)
		if err != nil {
			return opts.ConversationID, err
		}
		// Only the user message carries the turn's context.
		message, calls = answers, nil
		opts.Context, opts.CustomUserContext = nil, ""
	}
	return opts.ConversationID, ErrMaxTurns
}

// send streams one request and returns the client calls it paused on.
func (x *roundsExchange) send(ctx context.Context, message any, opts *assistant.SendOptions, emit func(exchangeEvent) bool) ([]ToolCall, error) {
	var calls []ToolCall
	id, err := x.backend.Send(ctx, message, *opts, func(response assistant.AssistantResponse) error {
		msg := response.Data.Attributes.StructuredMessage
		if msg.Content.Type == assistant.ContentClientToolCall {
			calls = append(calls, toolCallOf(msg.Content))
		}
		if !emit(exchangeEvent{message: &msg}) {
			return ctx.Err()
		}
		return nil
	})
	// Send may discover a server-assigned ID before a later stream failure or
	// cancellation. Report it so every surface has a resumable handle.
	if id != "" {
		opts.ConversationID = id
		if !emit(exchangeEvent{conversation: id}) && err == nil {
			err = ctx.Err()
		}
	}
	if err != nil {
		return nil, backendError{err}
	}
	return calls, nil
}

// collect hands out a round's calls and waits until every one is answered,
// returning the answers in call order.
func (x *roundsExchange) collect(ctx context.Context, calls []ToolCall, emit func(exchangeEvent) bool) ([]assistant.ClientToolResponse, error) {
	if !emit(exchangeEvent{execute: calls}) {
		return nil, ctx.Err()
	}
	answers := make([]assistant.ClientToolResponse, len(calls))
	for missing := len(calls); missing > 0; {
		select {
		case <-x.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		x.mu.Lock()
		answered := x.answered
		x.answered = nil
		x.mu.Unlock()
		for _, answer := range answered {
			i := slices.IndexFunc(calls, func(call ToolCall) bool { return call.ID == answer.ToolCallID })
			if i >= 0 && answers[i].ToolCallID == "" {
				answers[i] = answer
				missing--
			}
		}
	}
	return answers, nil
}

// A server tool, such as one starting a background task, can outlive its
// request, so ending request rounds does not settle it.
func (x *roundsExchange) final() bool { return false }

func (x *roundsExchange) answer(response assistant.ClientToolResponse) {
	x.mu.Lock()
	x.answered = append(x.answered, response)
	x.mu.Unlock()
	select {
	case x.ready <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// streamExchange keeps one multi-agent stream open and submits each answer
// independently, so a slow submission never delays sibling calls.
type streamExchange struct {
	backend DuplexBackend
	message string
	opts    assistant.SendOptions

	// Set by run before it emits any call, so before any answer.
	ctx  context.Context
	fail context.CancelCauseFunc
}

func (x *streamExchange) run(ctx context.Context, emit func(exchangeEvent) bool) (string, error) {
	ctx, x.fail = context.WithCancelCause(ctx)
	defer x.fail(nil)
	x.ctx = ctx
	conversation := x.opts.ConversationID
	err := x.backend.Stream(ctx, x.message, x.opts, func(response assistant.AssistantResponse) error {
		msg := response.Data.Attributes.StructuredMessage
		ev := exchangeEvent{message: &msg}
		if id := response.Data.Attributes.ConversationID; id != "" && id != conversation {
			conversation, ev.conversation = id, id
		}
		if msg.Content.Type == assistant.ContentClientToolCall {
			ev.execute = []ToolCall{toolCallOf(msg.Content)}
		}
		if !emit(ev) {
			return ctx.Err()
		}
		return nil
	})
	if cause := context.Cause(ctx); errors.As(cause, new(backendError)) {
		return conversation, cause
	}
	if err != nil {
		return conversation, backendError{err}
	}
	return conversation, nil
}

// The stream ends with the run, so its unfinished tools cannot report again.
func (x *streamExchange) final() bool { return true }

func (x *streamExchange) answer(response assistant.ClientToolResponse) {
	go func() {
		err := x.backend.SubmitToolResults(x.ctx, []assistant.ClientToolResponse{response})
		if err == nil {
			return
		}
		if resultMayBeObsolete(err) {
			// Root completion can retire child batches before the server
			// finishes cleanup and emits its authoritative terminal event.
			// Bound this grace period so a real conflict still fails.
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			select {
			case <-x.ctx.Done():
				return
			case <-timer.C:
			}
		}
		x.fail(backendError{err})
	}()
}

func resultMayBeObsolete(err error) bool {
	var apiErr *assistant.APIError
	return errors.Is(err, assistant.ErrNoActiveMultiAgentRun) ||
		errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusConflict || apiErr.StatusCode == http.StatusNotFound)
}

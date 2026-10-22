package agent

import (
	"context"
	"errors"
	"net/http"
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
// without blocking; it is called only after run emits that call.
type exchange interface {
	run(ctx context.Context, emit func(exchangeEvent) bool) (conversation string, err error)
	answer(assistant.ClientToolResponse)
}

// exchangeEvent is one backend fact, applied field by field. Only execute
// makes a client call run: content alone, whatever its type, never does.
type exchangeEvent struct {
	conversation string // server-assigned conversation ID
	message      *assistant.Message
	execute      *ToolCall
}

// backendError marks a failure at the Assistant backend boundary.
type backendError struct{ error }

func (e backendError) Unwrap() error { return e.error }

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
			call := toolCallOf(msg.Content)
			ev.execute = &call
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

package agent

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
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
// false when the turn stops listening, and is safe for concurrent use. answer
// submits an executed call's result without blocking; it is called only after
// run emits that call. final reports whether ending the exchange makes every
// streamed tool terminal. stop reports that the turn is stopping, so later
// requests only release the backend.
type exchange interface {
	run(ctx context.Context, emit func(exchangeEvent) bool) (conversation string, err error)
	answer(assistant.ClientToolResponse)
	withdraw(id string)
	final() bool
	stop()
}

// exchangeEvent is one backend fact, applied field by field. Only execute
// and delivered make a client call run: content alone, whatever its type, never does.
type exchangeEvent struct {
	// round starts this backend send, 1-based. Every send is announced
	// before it is made, so a turn cancelled earlier stays outside any round.
	round        int
	conversation string // the conversation's ID, once known or changed
	title        string // the conversation's newly saved title
	message      *assistant.Message
	execute      []ToolCall // a batch, dispatched at once
	// delivered are calls a send's response carried, run as they arrive.
	delivered []ToolCall
	// accepted reports the round's request was delivered.
	accepted bool
	// answered are the calls the round's request delivered answers to.
	answered []string
	// broken reports the round's response broke before the turn ended: the
	// calls it could not deliver are offered once the conversation shows
	// them.
	broken bool
	// err fails the turn at once, before run returns: the exchange cannot
	// continue, so its running calls must stop rather than await transport
	// cleanup.
	err error
}

// sendFailure is a request that failed before any response: the
// conversation may or may not hold its message. answers are the calls it
// answered, if it was not the turn's prompt.
type sendFailure struct {
	answers []string
	err     error
}

func (f *sendFailure) Error() string   { return f.err.Error() }
func (f *sendFailure) Unwrap() []error { return []error{ErrUndelivered, f.err} }

// backendError marks a failure at the Assistant backend boundary.
type backendError struct{ error }

func (e backendError) Unwrap() error { return e.error }

// roundsExchange sends one request per round: the user message, then the
// answers to each round's client calls. A send is a write: the round's
// content comes from the conversation subscription. The client calls it
// runs are only those its own request's response delivers, since that
// response reaches only this client and is never replayed; the
// subscription, which every session sees and which replays, never starts a
// call.
type roundsExchange struct {
	backend Backend
	message any
	opts    assistant.SendOptions
	resumed []ToolCall // answered in the first round, instead of sending message

	mu       sync.Mutex
	open     map[string]bool // the round's calls still to answer
	order    []string        // the round's calls, in the order they came
	answered []assistant.ClientToolResponse
	stopped  bool
	ready    chan struct{}
	// follows is false for a backend without a conversation subscription,
	// such as the multi-agent one sending a turn it cannot run: its send's
	// own response is then the round's content.
	follows bool
}

func newRoundsExchange(backend Backend, message any, opts assistant.SendOptions, resumed []ToolCall, follows bool) *roundsExchange {
	return &roundsExchange{
		backend: backend, message: message, opts: opts, resumed: resumed, open: map[string]bool{},
		ready: make(chan struct{}, 1), follows: follows,
	}
}

func (x *roundsExchange) run(ctx context.Context, emit func(exchangeEvent) bool) (string, error) {
	message, opts := x.message, x.opts
	var answers []string // the calls the round's send answers
	if x.resumed != nil {
		x.expect(ids(x.resumed))
		if !emit(exchangeEvent{round: 1}) || !emit(exchangeEvent{execute: x.resumed}) {
			return opts.ConversationID, ctx.Err()
		}
		responses, err := x.collect(ctx)
		if err != nil || len(responses) == 0 {
			return opts.ConversationID, err
		}
		message = responses
		for _, r := range responses {
			answers = append(answers, r.ToolCallID)
		}
	}
	first := 1
	if x.resumed != nil {
		first = 2
	}
	for round := first; round <= maxTurns; round++ {
		if !emit(exchangeEvent{round: round}) {
			return opts.ConversationID, ctx.Err()
		}
		if opts.ConversationID == "" {
			// A new conversation gets its id here, so it is known before the
			// first response and every surface has a resumable handle even if
			// the send fails.
			opts.ConversationID = newConversationID()
			if !emit(exchangeEvent{conversation: opts.ConversationID}) {
				return opts.ConversationID, ctx.Err()
			}
		}
		x.mu.Lock()
		if x.stopped {
			// The pending context waits for a request whose reply is shown.
			opts.Context, opts.CustomUserContext = nil, ""
		}
		x.mu.Unlock()
		var err error
		if x.follows {
			err = x.send(ctx, message, opts, answers, emit)
		} else {
			err = x.sendFolding(ctx, message, opts, emit)
		}
		if err != nil {
			return opts.ConversationID, err
		}
		if len(answers) > 0 && !emit(exchangeEvent{answered: answers}) {
			return opts.ConversationID, ctx.Err()
		}
		// Only the turn's first request carries its context.
		opts.Context, opts.CustomUserContext = nil, ""
		responses, err := x.collect(ctx)
		if err != nil || len(responses) == 0 {
			return opts.ConversationID, err
		}
		message, answers = responses, nil
		for _, r := range responses {
			answers = append(answers, r.ToolCallID)
		}
	}
	return opts.ConversationID, ErrMaxTurns
}

func ids(calls []ToolCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.ID)
	}
	return out
}

// expect adds calls the round must answer before its next request.
func (x *roundsExchange) expect(ids []string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	for _, id := range ids {
		x.open[id] = true
		x.order = append(x.order, id)
	}
}

// send writes one request and runs the client calls its response delivers,
// as they arrive; the round's content comes from the conversation
// subscription. The round ends when the response does. A response that
// breaks leaves the turn running on the server, and the calls it could not
// deliver wait for an explicit approval instead of running by themselves.
func (x *roundsExchange) send(ctx context.Context, message any, opts assistant.SendOptions, answers []string, emit func(exchangeEvent) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	posted := make(chan error, 1)
	var responded atomic.Bool
	var batch []ToolCall // calls delivered and not yet handed out, guarded by x.mu
	var saved string     // a title update not yet handed out, guarded by x.mu
	var failure error    // the turn's error a flagged message reported, guarded by x.mu
	arrived := make(chan struct{}, 1)
	// flush hands out what the response delivered, in arrival order. Each
	// call runs whichever session's turn made it: the response is this
	// client's to answer.
	accepted := false
	flush := func() bool {
		x.mu.Lock()
		calls, title := batch, saved
		batch, saved = nil, ""
		x.mu.Unlock()
		if responded.Load() && !accepted {
			accepted = true
			if !emit(exchangeEvent{accepted: true}) {
				return false
			}
		}
		return (len(calls) == 0 && title == "") || emit(exchangeEvent{delivered: calls, title: title})
	}
	go func() {
		_, err := x.backend.Send(ctx, message, opts, func(response assistant.AssistantResponse) error {
			// Emitting here could block the response behind the engine; the
			// round hands out what arrived from its own loop.
			responded.Store(true)
			x.mu.Lock()
			if err := response.Data.Attributes.StructuredMessage.Err(); err != nil {
				failure = cmp.Or(failure, err)
			}
			if title := response.Data.Attributes.ConversationTitle; title != "" {
				saved = title
			} else if msg := response.Data.Attributes.StructuredMessage; msg.Content.Type == assistant.ContentClientToolCall && msg.BackgroundTaskID == nil {
				call := toolCallOf(msg.Content)
				x.open[call.ID] = true
				x.order = append(x.order, call.ID)
				batch = append(batch, call)
			}
			x.mu.Unlock()
			signal(arrived)
			return nil
		})
		posted <- err
	}()
	for {
		select {
		case <-arrived:
			if !flush() {
				return ctx.Err()
			}
		case err := <-posted:
			if !flush() || ctx.Err() != nil {
				return ctx.Err()
			}
			switch {
			case err == nil:
				if !accepted && !emit(exchangeEvent{accepted: true}) {
					return ctx.Err()
				}
				x.mu.Lock()
				defer x.mu.Unlock()
				// The backend answered, so the failure is the turn's.
				return failure
			case errors.Is(err, assistant.ErrStreamBroken):
				// The turn runs on; the conversation shows the rest.
				if !emit(exchangeEvent{broken: true}) {
					return ctx.Err()
				}
				return nil
			case !responded.Load():
				// Never resent on its own: the conversation shows whether
				// it arrived, and the user decides to send it again.
				return backendError{&sendFailure{answers: answers, err: err}}
			default:
				return backendError{err}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// sendFolding streams one request whose response is the round's content,
// and runs the client calls it pauses on.
// A message flagged as an error fails the round once the stream ends, so its
// trailing usage still arrives and its calls never run.
func (x *roundsExchange) sendFolding(ctx context.Context, message any, opts assistant.SendOptions, emit func(exchangeEvent) bool) error {
	var calls []ToolCall
	var failure error
	_, err := x.backend.Send(ctx, message, opts, func(response assistant.AssistantResponse) error {
		ev := exchangeEvent{title: response.Data.Attributes.ConversationTitle}
		if ev.title == "" {
			msg := response.Data.Attributes.StructuredMessage
			if msg.Content.Type == assistant.ContentClientToolCall {
				calls = append(calls, toolCallOf(msg.Content))
			}
			if err := msg.Err(); err != nil {
				// The transcript shows the message apart from the answer.
				failure = cmp.Or(failure, err)
			}
			ev.message = &msg
		}
		if !emit(ev) {
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		return backendError{err}
	}
	if !emit(exchangeEvent{accepted: true}) {
		return ctx.Err()
	}
	if failure != nil {
		// The backend answered, so the failure is the turn's, not the backend's.
		return failure
	}
	x.expect(ids(calls))
	if len(calls) > 0 && !emit(exchangeEvent{execute: calls}) {
		return ctx.Err()
	}
	return nil
}

// collect waits until every call the round expects is answered or
// withdrawn, returning the answers in call order.
func (x *roundsExchange) collect(ctx context.Context) ([]assistant.ClientToolResponse, error) {
	var answers []assistant.ClientToolResponse
	defer func() {
		x.mu.Lock()
		defer x.mu.Unlock()
		slices.SortStableFunc(answers, func(a, b assistant.ClientToolResponse) int {
			return slices.Index(x.order, a.ToolCallID) - slices.Index(x.order, b.ToolCallID)
		})
		x.order = nil
	}()
	for {
		x.mu.Lock()
		for _, answer := range x.answered {
			if x.open[answer.ToolCallID] {
				delete(x.open, answer.ToolCallID)
				answers = append(answers, answer)
			}
		}
		x.answered = nil
		missing := len(x.open)
		x.mu.Unlock()
		if missing == 0 {
			return answers, nil
		}
		select {
		case <-x.ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// A server tool, such as one starting a background task, can outlive its
// request, so ending request rounds does not settle it.
func (x *roundsExchange) final() bool { return false }

func (x *roundsExchange) stop() {
	x.mu.Lock()
	x.stopped = true
	x.mu.Unlock()
}

// withdraw drops a call the round no longer answers, as one answered
// elsewhere first or already run.
func (x *roundsExchange) withdraw(id string) {
	x.mu.Lock()
	delete(x.open, id)
	x.mu.Unlock()
	select {
	case x.ready <- struct{}{}:
	default:
	}
}

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
	emit func(exchangeEvent) bool
}

func (x *streamExchange) run(ctx context.Context, emit func(exchangeEvent) bool) (string, error) {
	ctx, x.fail = context.WithCancelCause(ctx)
	defer x.fail(nil)
	x.ctx, x.emit = ctx, emit
	conversation := x.opts.ConversationID
	if !emit(exchangeEvent{round: 1}) {
		return conversation, ctx.Err()
	}
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

// A stream's calls are each answered on their own.
func (x *streamExchange) withdraw(string) {}

// The stream ends with the run, so its unfinished tools cannot report again.
func (x *streamExchange) final() bool { return true }

// The stream's only request opened it, so a stop has no request to change.
func (x *streamExchange) stop() {}

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
		// Cancelling the stream can wait on the server's cancellation, so
		// also fail the turn now, which stops the sibling calls at once.
		cause := backendError{err}
		x.fail(cause)
		x.emit(exchangeEvent{err: cause})
	}()
}

func resultMayBeObsolete(err error) bool {
	var apiErr *assistant.APIError
	return errors.Is(err, assistant.ErrNoActiveMultiAgentRun) ||
		errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusConflict || apiErr.StatusCode == http.StatusNotFound)
}

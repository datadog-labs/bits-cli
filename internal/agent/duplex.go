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

const duplexToolConcurrency = 16

type duplexStreamEvent struct {
	response assistant.AssistantResponse
	terminal bool
	err      error
}

type duplexStreamResult struct {
	conversationID string
	streamed       bool
	err            error
}

// All mutable fields and callbacks belong to the engine actor. Only the
// transport goroutines write the input channels.
type duplexToolStream struct {
	events           <-chan duplexStreamEvent
	submissionErrors <-chan error
	fold             func(assistant.AssistantResponse) bool
	submit           func(assistant.ClientToolResponse)
	cancel           context.CancelFunc
	terminal         bool
	backendFailure   bool
}

func (e *Engine) runDuplex(
	ctx context.Context,
	in TurnInput,
	backend DuplexBackend,
	user userRequest,
	generation uint64,
	send func(Event) bool,
) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	turnStart := len(e.transcript.Blocks())
	emit := func(event Event) bool {
		event.Round = 1
		if event.Kind == EventTranscript {
			event.Origin = TranscriptOriginRemote
		}
		return send(event)
	}
	finalizeAndEmit := func() bool {
		settled := e.cancelDuplexTools(in.Tools, turnStart)
		if !e.transcript.FinalizeAll() && !settled {
			return true
		}
		return emit(Event{Kind: EventTranscript, Transcript: e.snapshot()})
	}
	events := make(chan duplexStreamEvent)
	streamReturned := make(chan duplexStreamResult, 1)
	defer func() {
		cancel()
		// Keep the operation alive for bounded transport cleanup (including
		// the server cancellation request). Tool handlers are never joined.
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case result := <-streamReturned:
			if result.streamed || result.err == nil {
				e.delivery.delivered(user)
			}
			if result.conversationID != "" {
				e.opts.ConversationID = result.conversationID
			}
		case <-timer.C:
		}
	}()
	submissionErrors := make(chan error, 1)
	stream := &duplexToolStream{
		events:           events,
		submissionErrors: submissionErrors,
		cancel:           cancel,
		fold: func(response assistant.AssistantResponse) bool {
			if id := response.Data.Attributes.ConversationID; id != "" && id != e.opts.ConversationID {
				e.opts.ConversationID = id
				if !emit(Event{Kind: EventConversation, ConvID: id}) {
					return false
				}
			}
			return e.foldMessage(runCtx, in.Tools, response.Data.Attributes.StructuredMessage, emit)
		},
		submit: func(response assistant.ClientToolResponse) {
			// POST latency must not delay incoming sibling calls or terminal
			// events. Results are immutable values crossing this boundary.
			go func() {
				if err := backend.SubmitToolResults(runCtx, []assistant.ClientToolResponse{response}); err != nil {
					if duplexResultMayBeObsolete(err) {
						// Root completion can retire child batches before the server
						// finishes cleanup and emits its authoritative terminal event.
						// Bound this grace period so a real conflict still fails.
						timer := time.NewTimer(30 * time.Second)
						defer timer.Stop()
						select {
						case <-runCtx.Done():
							return
						case <-timer.C:
						}
					}
					select {
					case submissionErrors <- err:
					case <-runCtx.Done():
					}
				}
			}()
		},
	}
	go func() {
		var conversationID string
		streamed := false
		err := backend.Stream(runCtx, user.message, user.opts, func(response assistant.AssistantResponse) error {
			streamed = true
			if id := response.Data.Attributes.ConversationID; id != "" {
				conversationID = id
			}
			select {
			case events <- duplexStreamEvent{response: response}:
				return nil
			case <-runCtx.Done():
				return runCtx.Err()
			}
		})
		streamReturned <- duplexStreamResult{conversationID: conversationID, streamed: streamed, err: err}
		select {
		case events <- duplexStreamEvent{terminal: true, err: err}:
		case <-runCtx.Done():
		}
	}()
	_, err := e.runToolActor(runCtx, in.Tools, nil, generation, emit, in.OnDeny, stream)
	if err != nil && (ctx.Err() == nil || !errors.Is(err, ctx.Err())) {
		if !finalizeAndEmit() {
			return
		}
		emit(Event{Kind: EventError, Err: err, BackendFailure: stream.backendFailure})
		return
	}
	if ctx.Err() != nil {
		return
	}
	if !finalizeAndEmit() {
		return
	}
	emit(Event{Kind: EventTurnDone})
}

// cancelDuplexTools also settles provisional calls that never reached an
// execution-ready batch. Normalize their results to clear speculative UI state.
func (e *Engine) cancelDuplexTools(tools *ToolSet, from int) bool {
	changed := false
	for _, block := range e.transcript.Blocks()[from:] {
		if block.Tool == nil || block.Tool.Status.IsTerminal() {
			continue
		}
		call := ToolCall{ID: block.ToolCallID(), Name: block.Tool.Name, Input: block.Tool.Input}
		result := tools.NormalizeResult(call, cancelledResult())
		_, updated := e.transcript.MarkToolExecuted(call.ID, result)
		changed = changed || updated
	}
	return changed
}

func duplexResultMayBeObsolete(err error) bool {
	var apiErr *assistant.APIError
	return errors.Is(err, assistant.ErrNoActiveMultiAgentRun) ||
		errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusConflict || apiErr.StatusCode == http.StatusNotFound)
}

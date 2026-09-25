package agent

import (
	"context"
	"errors"
	"sync/atomic"
)

var ErrInputUnsupported = errors.New("user input is unsupported in noninteractive sessions")

// InputRequest carries an immutable prompt and a single-use reply channel.
// Its lifetime is bounded by the tool context, including per-tool cancellation.
type InputRequest struct {
	Value    any
	ctx      context.Context
	reply    chan any
	answered atomic.Bool
}

// Respond returns false for an already answered or cancelled request.
func (r *InputRequest) Respond(value any) bool {
	if r.ctx.Err() != nil || !r.answered.CompareAndSwap(false, true) {
		return false
	}
	r.reply <- value
	return true
}

// Pending reports whether the request can still receive an answer.
func (r *InputRequest) Pending() bool {
	return r.ctx.Err() == nil && !r.answered.Load()
}

type (
	inputContextKey struct{}
	inputSender     func(*InputRequest) error
)

// RequestInput waits for explicit user input, independently of tool permissions.
// Only interactive turns install a sender; headless handlers fail immediately.
func RequestInput(ctx context.Context, value any) (any, error) {
	send, ok := ctx.Value(inputContextKey{}).(inputSender)
	if !ok {
		return nil, ErrInputUnsupported
	}
	r := &InputRequest{Value: value, ctx: ctx, reply: make(chan any, 1)}
	if err := send(r); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case answer := <-r.reply:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return answer, nil
	}
}

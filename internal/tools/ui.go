package tools

import (
	"context"
	"fmt"

	"github.com/datadog-labs/bits-cli/internal/agent"
)

// UI hands interactive tool calls to a front end and waits for the user. It is
// headless: the front end builds its own component from Request.Call and
// answers with a value the tool turns into its result.
type UI struct {
	requests chan *Request
}

func NewUI() *UI {
	return &UI{requests: make(chan *Request)}
}

// Request is one tool call waiting on the user.
type Request struct {
	Call  agent.ToolCall
	ctx   context.Context
	reply chan uiReply
}

type uiReply struct {
	answer any
	err    error
}

// Context ends when the call is cancelled; the front end should then drop it.
func (r *Request) Context() context.Context { return r.ctx }

// Respond completes the request with the user's answer. Only the first
// response counts.
func (r *Request) Respond(answer any, err error) {
	select {
	case r.reply <- uiReply{answer: answer, err: err}:
	default:
	}
}

// Requests delivers calls to the front end in the order tools present them.
func (u *UI) Requests() <-chan *Request { return u.requests }

// Interact presents call and blocks until the front end answers with a T or
// ctx ends.
func Interact[T any](ctx context.Context, ui *UI, call agent.ToolCall) (T, error) {
	var zero T
	request := &Request{Call: call, ctx: ctx, reply: make(chan uiReply, 1)}
	select {
	case ui.requests <- request:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	select {
	case reply := <-request.reply:
		if reply.err != nil {
			return zero, reply.err
		}
		answer, ok := reply.answer.(T)
		if !ok {
			return zero, fmt.Errorf("%s: front end answered with %T, want %T", call.Name, reply.answer, zero)
		}
		return answer, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

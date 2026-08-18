// Package agent drives the remote assistant turn loop and emits fine-grained
// events on a channel. It imports only the assistant client and has no Bubble
// Tea / UI dependency, so it is reusable by a future headless surface and
// testable without a program.
package agent

import (
	"context"
	"errors"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// Backend is the minimal transport the engine drives. *assistant.Client
// satisfies it; tests can substitute a fake.
type Backend interface {
	Send(ctx context.Context, message any, opts assistant.SendOptions,
		fn func(assistant.AssistantResponse) error) (string, error)
}

// EventKind discriminates the events the engine streams for a turn.
type EventKind int

const (
	EventNone         EventKind = iota // ignored; keeps classify total
	EventDelta                         // text/reasoning fragment for ItemID
	EventTool                          // tool call or result upsert
	EventUsage                         // token accounting
	EventConversation                  // server-assigned/confirmed conversation id
	EventTurnDone                      // the turn completed with no pending tool calls
	EventError                         // the turn failed
)

// ToolCall is the agent-owned tool payload on an event. The tui maps it to a
// transcript view; agent stays independent of the UI-domain chat package.
type ToolCall struct {
	Name   string
	Input  string
	Output string
	Status string // wire status ("success" / "error"); empty for a fresh call
}

// Event is one thing that happened during a turn. It is a plain value carried
// on a channel, with only the fields relevant to Kind populated.
type Event struct {
	Kind    EventKind
	ItemID  string                // stable transcript key (see classify.itemID)
	Role    assistant.Role        // for EventDelta
	Content assistant.ContentKind // for EventDelta: text vs reasoning
	Text    string                // for EventDelta
	Tool    ToolCall              // for EventTool
	Usage   *assistant.Usage      // for EventUsage
	ConvID  string                // for EventConversation
	Err     error                 // for EventError
}

// maxTurns caps the client-tool loop so a misbehaving backend can't spin
// forever.
const maxTurns = 20

// Engine drives the assistant turn loop over a Backend and streams events. It
// is safe to create one Engine and run many turns sequentially.
type Engine struct {
	backend Backend
	tools   map[string]assistant.Tool // client tools by name; empty for the MVP
	opts    assistant.SendOptions
}

// New returns an Engine. opts carries the per-request defaults (profile, model,
// conversation id, …); ConversationID is updated as turns run.
func New(b Backend, opts assistant.SendOptions) *Engine {
	return &Engine{backend: b, tools: map[string]assistant.Tool{}, opts: opts}
}

// Start runs one user turn (plus any client-tool round-trips) in a goroutine
// and streams events. The channel is closed when the turn ends. Cancel ctx to
// interrupt; cancellation ends the turn quietly (no error event).
func (e *Engine) Start(ctx context.Context, message string) <-chan Event {
	out := make(chan Event, 64)
	go e.run(ctx, message, out)
	return out
}

func (e *Engine) run(ctx context.Context, message string, out chan<- Event) {
	defer close(out)

	// send is cancellation-aware so a stalled consumer during cancel can't
	// wedge the engine goroutine.
	send := func(ev Event) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var next any = message
	convID := e.opts.ConversationID

	for range maxTurns {
		var calls []assistant.Content

		opts := e.opts
		opts.ConversationID = convID
		opts.ClientTools = e.toolDefs()

		id, err := e.backend.Send(ctx, next, opts, func(ar assistant.AssistantResponse) error {
			ev, call := classify(ar)
			if call != nil {
				calls = append(calls, *call)
			}
			if ev.Kind != EventNone && !send(ev) {
				return ctx.Err()
			}
			return nil
		})
		if ctx.Err() != nil {
			return // cancelled: end the turn quietly
		}
		if err != nil {
			send(Event{Kind: EventError, Err: err})
			return
		}

		convID = id
		send(Event{Kind: EventConversation, ConvID: convID})

		if len(calls) == 0 {
			send(Event{Kind: EventTurnDone})
			return
		}
		next = e.execTools(ctx, calls)
	}
	send(Event{Kind: EventError, Err: errors.New("exceeded max turns")})
}

// toolDefs returns the client tool definitions to resend each turn (nil when
// none are registered).
func (e *Engine) toolDefs() []assistant.ClientTool {
	if len(e.tools) == 0 {
		return nil
	}
	defs := make([]assistant.ClientTool, 0, len(e.tools))
	for _, t := range e.tools {
		defs = append(defs, t.ClientTool)
	}
	return defs
}

// execTools runs each client tool call locally and builds the responses to post
// back. Unregistered tools answer with an error result rather than aborting the
// loop. No tools are registered in the MVP, so this is not reached yet.
func (e *Engine) execTools(ctx context.Context, calls []assistant.Content) []assistant.ClientToolResponse {
	responses := make([]assistant.ClientToolResponse, 0, len(calls))
	for _, call := range calls {
		var name, input string
		if call.Metadata != nil {
			name, input = call.Metadata.Name, call.Metadata.Input
		}
		resp := assistant.ClientToolResponse{
			Type:       "client_tool_response",
			ToolCallID: call.ToolCallID,
			Status:     "success",
			Metadata:   assistant.ClientToolMetadata{Name: name, Input: input},
		}
		switch tool, ok := e.tools[name]; {
		case !ok:
			resp.Status = "error"
			resp.Title = "Unknown tool"
			resp.Metadata.Output = "no client tool named " + name + " is registered"
		default:
			if out, err := tool.Run(ctx, input); err != nil {
				resp.Status = "error"
				resp.Title = "Tool error"
				resp.Metadata.Output = err.Error()
			} else {
				resp.Title = "Ran " + name
				resp.Metadata.Output = out
			}
		}
		responses = append(responses, resp)
	}
	return responses
}

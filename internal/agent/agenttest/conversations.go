// Package agenttest lets a test backend serve the conversation subscription
// the engine reads turns from, out of the responses its sends stream.
package agenttest

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// Conversations records each conversation's messages as a test backend's
// sends stream them, and serves its subscriptions: a replay of what the
// conversation holds, the replay marker, then live messages. The zero value
// is ready to use.
type Conversations struct {
	mu    sync.Mutex
	convs map[string]*conversation
	// shown signals that a subscriber handled what it was sent.
	shown *sync.Cond
}

type conversation struct {
	followed chan struct{} // closed once a subscription attached
	saved    []assistant.Message
	running  bool // a send's turn is in progress
	start    int  // where the running turn's messages start in saved
	subs     map[*subscriber]struct{}
}

type subscriber struct {
	queue   []assistant.Message
	ready   chan struct{}
	pending int // sent but not yet handled
}

func (c *Conversations) get(id string) *conversation {
	if c.shown == nil {
		c.shown = sync.NewCond(&c.mu)
	}
	if c.convs == nil {
		c.convs = map[string]*conversation{}
	}
	conv, ok := c.convs[id]
	if !ok {
		conv = &conversation{subs: map[*subscriber]struct{}{}, followed: make(chan struct{})}
		c.convs[id] = conv
	}
	return conv
}

func (conv *conversation) broadcast(msg assistant.Message) {
	for s := range conv.subs {
		s.pending++
		s.queue = append(s.queue, msg)
		select {
		case s.ready <- struct{}{}:
		default:
		}
	}
}

func marker(status string) assistant.Message {
	return assistant.AssistantMessage("turn-"+status, assistant.Content{
		Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: status},
	})
}

// userMessages is how the conversation holds a send's message.
func userMessages(message any, opts assistant.SendOptions) []assistant.Message {
	switch m := message.(type) {
	case string:
		msg := assistant.Message{Role: "user", MessageID: "user-" + m, Content: assistant.TextContent(m)}
		if opts.Context != nil && len(opts.Context.Entities) > 0 {
			entities, err := json.Marshal(opts.Context.Entities)
			if err != nil {
				panic(err) // entities are plain data
			}
			msg.ContextEntities = entities
		}
		return []assistant.Message{msg}
	case assistant.SteeredPrompt:
		return append(userMessages(m.Answers, assistant.SendOptions{}), userMessages(m.Text, opts)...)
	case []assistant.ClientToolResponse:
		var out []assistant.Message
		for _, r := range m {
			out = append(out, assistant.Message{Role: "user", MessageID: "answer-" + r.ToolCallID, Content: assistant.Content{
				Type: assistant.ContentClientToolResponse,
				Tool: &assistant.ToolPayload{ToolCallID: r.ToolCallID, Status: string(r.Status), Title: r.Title},
			}})
		}
		return out
	}
	return nil
}

// Send runs post, the backend's own response to a send, as the send's turn:
// the conversation holds the send's message and everything post streams,
// and its subscriptions show them live between turn markers. fn sees what
// post streams, as on the real send.
//
// Send waits for the conversation's first subscription, so tests see a turn
// live rather than in a replay.
func (c *Conversations) Send(ctx context.Context, message any, opts assistant.SendOptions, fn func(assistant.AssistantResponse) error,
	post func(func(assistant.AssistantResponse) error) (string, error),
) (string, error) {
	c.mu.Lock()
	conv := c.get(opts.ConversationID)
	c.mu.Unlock()
	select {
	case <-conv.followed:
	case <-ctx.Done():
		return opts.ConversationID, ctx.Err()
	}
	c.mu.Lock()
	for _, msg := range userMessages(message, opts) {
		conv.saved = append(conv.saved, msg)
		conv.broadcast(msg)
	}
	conv.running, conv.start = true, len(conv.saved)
	conv.broadcast(marker("started"))
	c.mu.Unlock()
	defer func() {
		// The turn ends before its response does, and the subscriptions
		// show it first, so a test sees the conversation free once a send
		// returns.
		c.mu.Lock()
		conv.running = false
		conv.broadcast(marker("ended"))
		c.waitShownLocked(ctx, conv)
		c.mu.Unlock()
	}()
	return post(func(ar assistant.AssistantResponse) error {
		msg := ar.Data.Attributes.StructuredMessage
		c.mu.Lock()
		conv.saved = append(conv.saved, msg)
		conv.broadcast(msg)
		c.waitShownLocked(ctx, conv)
		c.mu.Unlock()
		return fn(ar)
	})
}

// waitShownLocked waits until conv's subscribers handled what they were
// sent, so a test sees each streamed message reach the engine before the
// send goes on, as with a synchronous stream.
func (c *Conversations) waitShownLocked(ctx context.Context, conv *conversation) {
	stop := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.shown.Broadcast()
	})
	defer stop()
	for ctx.Err() == nil {
		busy := false
		for s := range conv.subs {
			busy = busy || s.pending > 0
		}
		if !busy {
			return
		}
		c.shown.Wait()
	}
}

// Seed makes a conversation hold messages, as an earlier session left it.
func (c *Conversations) Seed(id string, messages ...assistant.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conv := c.get(id)
	conv.saved = append(conv.saved, messages...)
}

// Subscribe serves a conversation subscription until ctx ends. A
// conversation no send has named yet replays empty.
func (c *Conversations) Subscribe(ctx context.Context, id string, fn func(assistant.Message) error) error {
	c.mu.Lock()
	conv := c.get(id)
	replay := append([]assistant.Message(nil), conv.saved...)
	if conv.running {
		replay = append(replay[:conv.start:conv.start], marker("started"))
		replay = append(replay, conv.saved[conv.start:]...)
	}
	s := &subscriber{ready: make(chan struct{}, 1)}
	conv.subs[s] = struct{}{}
	select {
	case <-conv.followed:
	default:
		close(conv.followed)
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(conv.subs, s)
		c.shown.Broadcast()
		c.mu.Unlock()
	}()
	replay = append(replay, assistant.ReplayCompleteMessage())
	for _, msg := range replay {
		if err := fn(msg); err != nil {
			return err
		}
	}
	for {
		select {
		case <-s.ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		c.mu.Lock()
		queue := s.queue
		s.queue = nil
		c.mu.Unlock()
		for _, msg := range queue {
			err := fn(msg)
			c.mu.Lock()
			s.pending--
			c.shown.Broadcast()
			c.mu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

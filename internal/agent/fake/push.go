package fake

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// push_conversation() runs scripts, whose environment holds the built-ins, so
// it joins them at init time rather than in their initializer.
func init() {
	builtins["push_conversation"] = starlark.NewBuiltin("push_conversation", builtinPushConversation)
}

// pushedTurn is one user turn of a pushed conversation: script source, or a
// function whose name describes it.
type pushedTurn struct {
	text string
	fn   starlark.Callable
}

// PushConversation records a new conversation as if an earlier session had
// sent each script as a user turn. No engine runs: output is recorded, and a
// call() leaves its round unanswered, as when the user quit while a tool was
// waiting. An empty title uses the first turn's text. It returns the new
// conversation's id.
func (f *Fake) PushConversation(ctx context.Context, title string, scripts ...string) (string, error) {
	turns := make([]pushedTurn, len(scripts))
	for i, script := range scripts {
		turns[i] = pushedTurn{text: script}
	}
	return f.pushConversation(ctx, title, turns)
}

func (f *Fake) pushConversation(ctx context.Context, title string, turns []pushedTurn) (string, error) {
	if len(turns) == 0 {
		return "", errors.New("push_conversation: at least one turn is required")
	}
	c := f.conversation("")
	for _, t := range turns {
		out := &emitter{
			ctx: ctx, convID: c.id,
			fn:     func(assistant.AssistantResponse) error { return nil },
			record: func(m assistant.Message) { f.record(c, m) },
			nextID: f.nextID,
		}
		if err := f.runScript(c, out, assistant.SendOptions{ConversationID: c.id}, f.startTurn(c, t.text, t.fn)); err != nil {
			return "", err
		}
	}
	if title != "" {
		f.mu.Lock()
		c.title = truncateRunes(title, titleRunes)
		f.mu.Unlock()
	}
	return c.id, nil
}

// builtinPushConversation records a new conversation whose user turns are
// the given functions and returns its id. A replayed round returns the id it
// pushed before instead of pushing again.
func builtinPushConversation(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var title string
	if err := starlark.UnpackArgs(b.Name(), nil, kwargs, "title?", &title); err != nil {
		return nil, err
	}
	turns := make([]pushedTurn, len(args))
	for i, arg := range args {
		fn, ok := arg.(starlark.Callable)
		if !ok {
			return nil, fmt.Errorf("%s: turn %d is a %s, want a function", b.Name(), i+1, arg.Type())
		}
		// A turn is called again when its conversation resumes, so it must
		// see the same values then: freeze what it can reach.
		fn.Freeze()
		if function, ok := fn.(*starlark.Function); ok {
			function.Globals().Freeze()
		}
		turns[i] = pushedTurn{text: fn.Name() + "()", fn: fn}
	}
	r := runOf(thread)
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	i := r.pushes
	r.pushes++
	if i < len(r.pushed) {
		return starlark.String(r.pushed[i]), nil
	}
	id, err := r.fake.pushConversation(r.out.ctx, title, turns)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	r.fake.rememberPush(r.conv, id)
	return starlark.String(id), nil
}

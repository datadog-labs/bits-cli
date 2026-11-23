package fake

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"go.starlark.net/starlark"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func init() {
	builtins["drop"] = starlark.NewBuiltin("drop", builtinDrop)
	builtins["offline"] = starlark.NewBuiltin("offline", builtinOffline)
	builtins["restart"] = starlark.NewBuiltin("restart", builtinRestart)
	builtins["send_from_elsewhere"] = starlark.NewBuiltin("send_from_elsewhere", builtinSendFromElsewhere)
	builtins["answer_from_elsewhere"] = starlark.NewBuiltin("answer_from_elsewhere", builtinAnswerFromElsewhere)
	builtins["lose_turn_status"] = starlark.NewBuiltin("lose_turn_status", builtinLoseTurnStatus)
}

// builtinAnswerFromElsewhere answers, after a delay, the client calls of
// the conversation's paused turns as if another session ran them.
func builtinAnswerFromElsewhere(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	after := "1s"
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "after?", &after); err != nil {
		return nil, err
	}
	delay, err := time.ParseDuration(after)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	r, live, err := liveRun(thread, b)
	if err != nil || !live {
		return starlark.None, err
	}
	f, c := r.fake, r.conv
	time.AfterFunc(delay, func() {
		f.mu.Lock()
		var answers [][]assistant.ClientToolResponse
		for _, t := range c.live.paused {
			var rs []assistant.ClientToolResponse
			for _, id := range t.calls {
				if !f.answeredLocked(c, id) {
					rs = append(rs, assistant.ClientToolResponse{Type: "client_tool_response", ToolCallID: id, Title: "Answered elsewhere", Status: assistant.ToolStatusSuccess})
				}
			}
			if len(rs) > 0 {
				answers = append(answers, rs)
			}
		}
		f.mu.Unlock()
		for _, rs := range answers {
			f.enqueue(c, job{message: rs, opts: assistant.SendOptions{ConversationID: c.id}})
		}
	})
	return starlark.None, nil
}

// builtinLoseTurnStatus makes the next turn marker with the given status
// ("started" or "ended") reach no listener live, as when a listener falls
// behind; a replay still shows it.
func builtinLoseTurnStatus(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	status := "started"
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "status?", &status); err != nil {
		return nil, err
	}
	if status != "started" && status != "ended" {
		return nil, fmt.Errorf("%s: status must be \"started\" or \"ended\"", b.Name())
	}
	r, live, err := liveRun(thread, b)
	if err != nil || !live {
		return starlark.None, err
	}
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	if r.conv.live.lose == nil {
		r.conv.live.lose = map[string]int{}
	}
	r.conv.live.lose[status]++
	return starlark.None, nil
}

// answeredLocked reports whether c's history answers call id.
func (f *Fake) answeredLocked(c *conversation, id string) bool {
	return slices.ContainsFunc(c.messages, func(m assistant.Message) bool {
		return m.Content.Type == assistant.ContentClientToolResponse && m.Content.Tool != nil && m.Content.Tool.ToolCallID == id
	})
}

// liveRun returns the thread's run when its built-in should act now: not
// while loading a module, and not while replaying an answered round.
func liveRun(thread *starlark.Thread, b *starlark.Builtin) (*run, bool, error) {
	r := runOf(thread)
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, false, err
	}
	return r, r.live(), nil
}

// builtinDrop breaks every open stream while the turn carries on.
func builtinDrop(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	r, live, err := liveRun(thread, b)
	if err != nil || !live {
		return starlark.None, err
	}
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	r.fake.dropLocked()
	return starlark.None, nil
}

// builtinOffline drops every stream and fails every request for the given
// duration, while the turn carries on.
func builtinOffline(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var d string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 1, &d); err != nil {
		return nil, err
	}
	duration, err := time.ParseDuration(d)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	r, live, err := liveRun(thread, b)
	if err != nil || !live {
		return starlark.None, err
	}
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	r.fake.offlineUntil = time.Now().Add(duration)
	r.fake.dropLocked()
	return starlark.None, nil
}

// builtinRestart makes the service lose this turn and the conversation's
// queue without recording that they ended, and ends the script.
func builtinRestart(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 0); err != nil {
		return nil, err
	}
	r, live, err := liveRun(thread, b)
	if err != nil || !live {
		return starlark.None, err
	}
	r.fake.mu.Lock()
	defer r.fake.mu.Unlock()
	r.fake.restartLocked(r.conv)
	return nil, &abort{errRestarted}
}

// builtinSendFromElsewhere queues a prompt in this conversation as if
// another session sent it, after an optional delay. With simultaneous, it
// is queued just ahead of this session's next request instead, as when two
// clients send at the same instant.
func builtinSendFromElsewhere(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var script, after string
	var simultaneous bool
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "script", &script, "after?", &after, "simultaneous?", &simultaneous); err != nil {
		return nil, err
	}
	var delay time.Duration
	if after != "" {
		var err error
		if delay, err = time.ParseDuration(after); err != nil {
			return nil, fmt.Errorf("%s: %w", b.Name(), err)
		}
	}
	r, live, err := liveRun(thread, b)
	if err != nil || !live {
		return starlark.None, err
	}
	f, c := r.fake, r.conv
	j := job{message: script, opts: assistant.SendOptions{ConversationID: c.id}}
	queue := func() { f.enqueue(c, j) }
	if simultaneous {
		f.mu.Lock()
		c.live.racing = append(c.live.racing, j)
		f.mu.Unlock()
		return starlark.None, nil
	}
	if delay == 0 {
		queue()
	} else {
		time.AfterFunc(delay, queue)
	}
	return starlark.None, nil
}

// The fake conversation service runs each conversation's prompts one at a
// time on its own goroutine, apart from the requests that sent them: closing
// a send only stops listening, a stop is a separate request, and a
// subscription replays the conversation and then follows it live. Its
// built-ins can also break the network, so reconnects can be exercised
// offline.

// errDropped breaks open streams; the turn behind them carries on.
var errDropped = fmt.Errorf("%w: fake connection dropped", assistant.ErrStreamBroken)

// errRestarted ends the script that restarted the service.
var errRestarted = errors.New("fake: service restarted")

// subscriberBuffer bounds a listener's backlog; a listener that falls this
// far behind is dropped.
const subscriberBuffer = 1 << 16

// liveState is a conversation's running and queued prompts and their
// listeners. It is guarded by Fake.mu.
type liveState struct {
	subs    map[*subscriber]struct{}
	pending []job
	running *runningTurn // nil while idle
	serving bool         // a goroutine is running the prompts
	paused  []*scriptTurn
	racing  []job // queued ahead of this session's next request
	// lose counts, by status, the next turn markers that reach no
	// listener live.
	lose map[string]int
}

// job is one queued prompt: user text or the answers to a paused turn's
// client calls. origin is the send that queued it, nil when it came from
// another session.
type job struct {
	message any
	opts    assistant.SendOptions
	origin  *subscriber
	echoes  []assistant.Message // what the conversation shows of it while queued
}

// runningTurn is the prompt in progress: the history before it and what it
// has streamed so far, which a subscription replays.
type runningTurn struct {
	base    int
	log     []assistant.Message
	cancel  context.CancelFunc
	stopped bool // a stop request ended it
	lost    bool // a restart lost it
}

// item is one thing a listener receives.
type item struct {
	msg   assistant.Message
	title string // a title update, which carries no message
	err   error
	idle  bool // the conversation has nothing left to run
}

// subscriber is one listening request: a send (post), or a subscription.
type subscriber struct {
	post bool
	// pending counts what a subscription was handed and has not finished
	// handling.
	pending int
	ch      chan item
	broken  chan struct{} // closed when the connection breaks
	err     error         // why, set before broken closes
}

func newSubscriber(post bool) *subscriber {
	return &subscriber{post: post, ch: make(chan item, subscriberBuffer), broken: make(chan struct{})}
}

// breakLocked fails s at once with err.
func (s *subscriber) breakLocked(err error) {
	select {
	case <-s.broken:
	default:
		s.err = err
		close(s.broken)
	}
}

// deliverLocked hands it to s, breaking s when it fell too far behind.
func (s *subscriber) deliverLocked(it item) {
	select {
	case s.ch <- it:
		if !s.post {
			s.pending++
		}
	default:
		s.breakLocked(errDropped)
	}
}

// next waits for s's next item.
func (s *subscriber) next(ctx context.Context, idle <-chan time.Time) (item, error) {
	// A broken connection delivers nothing more, even what was in flight.
	select {
	case <-s.broken:
		return item{}, s.err
	default:
	}
	select {
	case it := <-s.ch:
		return it, nil
	case <-s.broken:
		return item{}, s.err
	case <-ctx.Done():
		return item{}, ctx.Err()
	case <-idle:
		return item{idle: true}, nil
	}
}

// shownCond signals that a subscription finished handling a message.
func (f *Fake) shownCond() *sync.Cond {
	if f.shown == nil {
		f.shown = sync.NewCond(&f.mu)
	}
	return f.shown
}

// waitShown waits until c's subscriptions handled everything they were
// handed, as the conversation shows a turn's end before its response ends.
func (f *Fake) waitShown(ctx context.Context, c *conversation) {
	stop := context.AfterFunc(ctx, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.shownCond().Broadcast()
	})
	defer stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for ctx.Err() == nil && slices.ContainsFunc(slices.Collect(maps.Keys(c.live.subs)), func(s *subscriber) bool { return s.pending > 0 }) {
		f.shownCond().Wait()
	}
}

// offlineLocked fails a request made while the network is down, before any
// response.
func (f *Fake) offlineLocked() error {
	if time.Now().Before(f.offlineUntil) {
		return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("fake network is offline")}
	}
	return nil
}

// Send queues a user message, or the answers to a paused turn's client
// calls, and streams the conversation until it has nothing left to run.
// Cancelling ctx only stops listening: the prompt keeps running.
func (f *Fake) Send(ctx context.Context, message any, opts assistant.SendOptions,
	fn func(assistant.AssistantResponse) error,
) (string, error) {
	f.mu.Lock()
	if err := f.offlineLocked(); err != nil {
		f.mu.Unlock()
		return opts.ConversationID, err
	}
	f.mu.Unlock()
	c := f.conversation(opts.ConversationID)
	if p, ok := message.(assistant.SteeredPrompt); ok {
		// The answers to a stopped turn's calls are saved before the prompt.
		f.mu.Lock()
		for _, r := range p.Answers {
			msg := toolResponseMessage(f.nextID(), r)
			f.appendLocked(c, msg)
			f.broadcastLocked(c, msg, subscriptions)
		}
		f.mu.Unlock()
		message = p.Text
	}
	switch m := message.(type) {
	case string:
	case []assistant.ClientToolResponse:
		// The real backend would persist it again on the tool answers.
		if opts.Context != nil {
			return c.id, errors.New("fake: tool responses must not carry context")
		}
		if f.pausedTurn(c, m) < 0 {
			return c.id, errors.New("fake: tool responses without a turn")
		}
	default:
		return c.id, fmt.Errorf("fake: unsupported message type %T", message)
	}
	f.mu.Lock()
	racing := c.live.racing
	c.live.racing = nil
	f.mu.Unlock()
	for _, j := range racing {
		f.enqueue(c, j)
	}
	sub := newSubscriber(true)
	for _, m := range f.byTurn(c, message) {
		f.enqueue(c, job{message: m, opts: opts, origin: sub})
	}
	defer f.unsubscribe(c, sub)
	for {
		it, err := sub.next(ctx, nil)
		switch {
		case err != nil:
			return c.id, err
		case it.err != nil:
			f.waitShown(ctx, c)
			return c.id, it.err
		case it.idle:
			f.waitShown(ctx, c)
			return c.id, nil
		case it.title != "":
			if err := fn(titleResp(c.id, it.title)); err != nil {
				return c.id, err
			}
			continue
		}
		if err := fn(resp(c.id, it.msg)); err != nil {
			return c.id, err
		}
	}
}

// byTurn splits answers into one message per paused turn they answer, in
// order: one request can answer calls of several turns, as when calls of
// another session's turn arrived on this client's send.
func (f *Fake) byTurn(c *conversation, message any) []any {
	rs, ok := message.([]assistant.ClientToolResponse)
	if !ok || len(rs) < 2 {
		return []any{message}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var turns []int
	groups := map[int][]assistant.ClientToolResponse{}
	for _, r := range rs {
		i := pausedIndexLocked(c, []assistant.ClientToolResponse{r})
		if _, seen := groups[i]; !seen {
			turns = append(turns, i)
		}
		groups[i] = append(groups[i], r)
	}
	out := make([]any, 0, len(turns))
	for _, i := range turns {
		out = append(out, groups[i])
	}
	return out
}

// pausedTurn returns the index of the paused turn rs answers, or -1.
func (f *Fake) pausedTurn(c *conversation, rs []assistant.ClientToolResponse) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return pausedIndexLocked(c, rs)
}

// pausedIndexLocked finds the paused turn whose calls rs answers. Answers
// matching no call resume the latest paused turn, and failing that the
// current one, as a hand-written script might answer. The current turn's
// index is len(paused).
func pausedIndexLocked(c *conversation, rs []assistant.ClientToolResponse) int {
	if len(rs) > 0 {
		if i := slices.IndexFunc(c.live.paused, func(t *scriptTurn) bool { return slices.Contains(t.calls, rs[0].ToolCallID) }); i >= 0 {
			return i
		}
	}
	switch {
	case len(c.live.paused) > 0:
		return len(c.live.paused) - 1
	case c.turn != nil && c.live.running == nil:
		return len(c.live.paused)
	}
	return -1
}

// enqueue queues j, shows its user messages to the other listeners at once, and
// starts running prompts when the conversation is idle.
func (f *Fake) enqueue(c *conversation, j job) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j.origin != nil {
		f.subscribeLocked(c, j.origin)
	}
	others := func(s *subscriber) bool { return s != j.origin }
	var echoes []assistant.Message
	switch m := j.message.(type) {
	case string:
		echoes = append(echoes, assistant.Message{Role: "user", MessageID: f.liveID(), Content: assistant.TextContent(m)})
	case []assistant.ClientToolResponse:
		for _, r := range m {
			echoes = append(echoes, toolResponseMessage(f.liveID(), r))
		}
	}
	for _, echo := range echoes {
		f.broadcastLocked(c, echo, others)
		if rt := c.live.running; rt != nil {
			// A subscription replays the prompts queued during the turn in
			// progress where it showed them live.
			rt.log = append(rt.log, echo)
		}
	}
	j.echoes = echoes
	c.live.pending = append(c.live.pending, j)
	if !c.live.serving {
		c.live.serving = true
		go f.serve(c)
	}
}

func (f *Fake) subscribeLocked(c *conversation, s *subscriber) {
	if c.live.subs == nil {
		c.live.subs = make(map[*subscriber]struct{})
	}
	c.live.subs[s] = struct{}{}
}

func (f *Fake) unsubscribe(c *conversation, s *subscriber) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(c.live.subs, s)
	f.shownCond().Broadcast()
}

// broadcastLocked delivers msg to the listeners to selects.
func (f *Fake) broadcastLocked(c *conversation, msg assistant.Message, to func(*subscriber) bool) {
	for s := range c.live.subs {
		if to(s) {
			s.deliverLocked(item{msg: msg})
		}
	}
}

// markLocked shows a turn marker to the subscriptions, unless it is one
// lose_turn_status() said to lose.
func (f *Fake) markLocked(c *conversation, marker assistant.Message) {
	if status := marker.Content.TurnStatus.Status; c.live.lose[status] > 0 {
		c.live.lose[status]--
		return
	}
	f.broadcastLocked(c, marker, subscriptions)
}

// subscriptions selects the listeners that are not sends.
func subscriptions(s *subscriber) bool { return !s.post }

func everyone(*subscriber) bool { return true }

// liveID names a message that is never saved, apart from the ids scripts
// see, so live-only messages do not shift them.
func (f *Fake) liveID() string { return "fake-live-" + strconv.FormatInt(f.liveSeq.Add(1), 10) }

// marker is a turn_status message, which only subscriptions receive.
func (f *Fake) marker(status string) assistant.Message {
	return assistant.AssistantMessage(f.liveID(), assistant.Content{
		Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: status},
	})
}

// serve runs c's prompts in order until none is left.
func (f *Fake) serve(c *conversation) {
	for f.runNext(c) {
	}
}

// runNext runs c's next prompt, and reports whether to keep serving.
func (f *Fake) runNext(c *conversation) bool {
	f.mu.Lock()
	if len(c.live.pending) == 0 {
		c.live.serving, c.live.running = false, nil
		for s := range c.live.subs {
			if s.post {
				s.deliverLocked(item{idle: true})
			}
		}
		f.mu.Unlock()
		return false
	}
	j := c.live.pending[0]
	c.live.pending = c.live.pending[1:]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := &runningTurn{base: len(c.messages), cancel: cancel}
	var turn *scriptTurn
	switch m := j.message.(type) {
	case string:
		turn = f.startTurnLocked(c, m, nil, j.opts.Context)
	case []assistant.ClientToolResponse:
		turn = f.resumeTurnLocked(c, m)
	}
	c.live.running = rt
	rt.log = slices.Clone(c.messages[rt.base:])
	started := f.marker("started")
	rt.log = append(rt.log, started)
	f.markLocked(c, started)
	f.mu.Unlock()

	out := &emitter{
		ctx: ctx, delay: f.Delay, convID: c.id,
		fn: func(ar assistant.AssistantResponse) error {
			msg := ar.Data.Attributes.StructuredMessage
			f.mu.Lock()
			defer f.mu.Unlock()
			if rt.lost || rt.stopped {
				return context.Canceled
			}
			if msg.Content.Type == assistant.ContentClientToolCall && msg.Content.Tool != nil {
				turn.calls = append(turn.calls, msg.Content.Tool.ToolCallID)
			}
			rt.log = append(rt.log, msg)
			f.broadcastLocked(c, msg, everyone)
			return nil
		},
		record: func(m assistant.Message) { f.record(c, m) },
		nextID: f.nextID,
	}
	var err error
	if turn == nil {
		err = errors.New("fake: tool responses without a turn")
	} else {
		turn.calls = nil
		err = f.runScript(c, out, j.opts, *turn)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if rt.lost {
		return false
	}
	if err != nil && !rt.stopped {
		if j.origin != nil {
			j.origin.deliverLocked(item{err: err})
		}
		failed := assistant.AssistantMessage(f.nextID(), assistant.TextContent(err.Error()))
		failed.IsError = true
		f.appendLocked(c, failed)
		f.broadcastLocked(c, failed, func(s *subscriber) bool { return s != j.origin })
	}
	if _, prompt := j.message.(string); prompt && err == nil && turn.index == 0 && j.origin != nil {
		// Like the server, replace the fallback title with a generated one
		// once the conversation starts, and stream it on the send.
		c.title = generatedTitle(len(f.convs))
		j.origin.deliverLocked(item{title: c.title})
	}
	if turn != nil && len(turn.calls) > 0 && err == nil {
		c.live.paused = append(c.live.paused, turn)
	}
	c.live.running = nil
	f.markLocked(c, f.marker("ended"))
	return true
}

// Stop ends the conversation's running prompt and records that it stopped.
func (f *Fake) Stop(_ context.Context, conversationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.offlineLocked(); err != nil {
		return err
	}
	c, ok := f.convs[conversationID]
	if !ok {
		return &assistant.APIError{StatusCode: http.StatusNotFound, Title: "Not Found"}
	}
	if rt := c.live.running; rt != nil && !rt.stopped {
		rt.stopped = true
		rt.cancel()
		stop := assistant.AssistantMessage(f.nextID(), assistant.Content{Type: assistant.ContentUserStop, Stop: &assistant.StopPayload{}})
		f.appendLocked(c, stop)
		rt.log = append(rt.log, stop)
		f.broadcastLocked(c, stop, everyone)
	}
	return nil
}

// subscribe replays the conversation: its history, then what the prompt in
// progress has streamed, then one replay_complete marker, then live messages
// until the stream breaks or stays silent for IdleClose. Background task
// messages are left to the task's own subscription.
func (f *Fake) subscribe(ctx context.Context, conversationID string, fn func(assistant.Message) error) error {
	f.mu.Lock()
	if err := f.offlineLocked(); err != nil {
		f.mu.Unlock()
		return err
	}
	c, ok := f.convs[conversationID]
	if !ok {
		f.mu.Unlock()
		return &assistant.APIError{StatusCode: http.StatusNotFound, Title: "Not Found"}
	}
	replay := slices.Clone(c.messages)
	if rt := c.live.running; rt != nil {
		replay = append(replay[:rt.base:rt.base], rt.log...)
	}
	// The conversation shows a queued prompt as soon as it is queued.
	for _, j := range c.live.pending {
		for _, echo := range j.echoes {
			if !slices.ContainsFunc(replay, func(m assistant.Message) bool { return m.MessageID == echo.MessageID }) {
				replay = append(replay, echo)
			}
		}
	}
	sub := newSubscriber(false)
	f.subscribeLocked(c, sub)
	f.mu.Unlock()
	defer f.unsubscribe(c, sub)

	replay = append(replay, assistant.ReplayCompleteMessage())
	for _, msg := range replay {
		if msg.BackgroundTaskID != nil {
			continue
		}
		if err := fn(msg); err != nil {
			return err
		}
	}
	var timer *time.Timer
	var idle <-chan time.Time
	if f.IdleClose > 0 {
		timer = time.NewTimer(f.IdleClose)
		defer timer.Stop()
		idle = timer.C
	}
	for {
		it, err := sub.next(ctx, idle)
		switch {
		case err != nil:
			return err
		case it.idle:
			return nil
		}
		err = fn(it.msg)
		f.mu.Lock()
		sub.pending--
		f.shownCond().Broadcast()
		f.mu.Unlock()
		if err != nil {
			return err
		}
		if timer != nil {
			timer.Reset(f.IdleClose)
		}
	}
}

// dropLocked breaks every open stream; prompts keep running.
func (f *Fake) dropLocked() {
	for _, c := range f.convs {
		for s := range c.live.subs {
			s.breakLocked(errDropped)
		}
	}
}

// restartLocked loses c's prompt in progress and its queue, without
// recording that they ended, and breaks every open stream.
func (f *Fake) restartLocked(c *conversation) {
	if rt := c.live.running; rt != nil {
		rt.lost = true
		rt.cancel()
	}
	c.live.running, c.live.pending, c.live.serving = nil, nil, false
	f.dropLocked()
}

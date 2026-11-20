package agent

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
)

// TurnState is whether the bound conversation has a turn running, wherever it
// started, as its subscription last reported.
type TurnState int

const (
	TurnIdle TurnState = iota
	TurnRunning
	TurnStopped // the last turn was stopped
)

// ConnectionState is how the engine is following its bound conversation.
type ConnectionState int

const (
	ConnectionIdle         ConnectionState = iota // no conversation to follow
	ConnectionConnecting                          // waiting for the first replay
	ConnectionConnected                           // replayed and following live
	ConnectionReconnecting                        // the last attempt failed; retrying
	ConnectionDisconnected                        // the conversation refused to be followed; see Err
	ConnectionNew                                 // the conversation does not exist yet; retrying until its first prompt is accepted
)

// Connection is the state of the conversation subscription.
type Connection struct {
	State ConnectionState
	// Since is when State began.
	Since time.Time
	// Failures counts the attempts that failed in a row.
	Failures int
	// Err is the last failure, if any.
	Err error
}

const (
	// retryBase is the first wait after a failed attempt, and the wait after
	// a stream that closed cleanly once it was live.
	retryBase = time.Second
	// retryMax caps the wait, doubling from retryBase, as the web client's
	// does. Sending a message tries again at once.
	retryMax = 30 * time.Second
)

var (
	// errReplayProtocol reports a stream that marked its replay complete
	// twice, or as a kind of replay this client does not know.
	errReplayProtocol = errors.New("conversation stream sent an unexpected replay marker")
	// errRefresh ends an attempt so a fresh replay replaces what it showed.
	errRefresh = errors.New("refresh the conversation")
	// ErrUndelivered reports a request that failed before any response.
	ErrUndelivered = errors.New("the message may not have reached the conversation")
)

// failedSend is a send that failed with no response: its prompt, put back
// at the front of the queue, or the calls it answered. after counts the
// prompts the conversation held when it failed.
type failedSend struct {
	prompt  *QueuedPrompt
	answers []string
	after   int
}

// resetDecisions drops what the bound conversation's recovery holds: a
// failed send and the decisions on calls a stop left unanswered, which
// belong to that conversation only.
func (e *Engine) resetDecisions() {
	e.failed, e.broken = nil, false
	e.stoppedCalls, e.approved, e.admitted, e.steering, e.unsettled = nil, nil, nil, nil, nil
}

// confirmFailed clears the failed send once the conversation shows its
// message arrived after all, as the web client does: a prompt by its text,
// answers by their call ids. A confirmed prompt leaves the queue, so it is
// never sent twice.
func (e *Engine) confirmFailed() {
	f := e.failed
	if f == nil {
		return
	}
	tr := e.transcript
	switch {
	case f.prompt != nil:
		found := false
		for _, b := range tr.blocks {
			n, err := strconv.Atoi(b.ID.Key)
			if b.ID.Scope == ScopePrompt && err == nil && n > f.after && b.Markdown != nil && b.Markdown.Content == f.prompt.Message {
				found = true
				break
			}
		}
		if !found {
			return
		}
		id := f.prompt.ID
		e.queue = slices.DeleteFunc(e.queue, func(q QueuedPrompt) bool { return q.ID == id })
	case len(f.answers) > 0:
		for _, id := range f.answers {
			if !tr.answered(id) {
				return
			}
		}
	}
	e.failed = nil
	e.publishLive()
}

// syncCalls brings the turn's calls in line with the conversation: a call
// answered elsewhere is no longer offered, and each call's block shows how
// it is doing here.
func (t *turn) syncCalls() {
	t.dropAnsweredElsewhere()
	t.showCalls()
}

// follower is the engine's subscription to its bound conversation. Only the
// actor reads or changes it, apart from its channels.
type follower struct {
	conversation string
	stop         context.CancelFunc
	wake         chan struct{} // Reconnect, once retrying stopped
	refresh      chan struct{} // ask for a fresh replay
	// generation counts refreshes; an attempt started before the latest one
	// is stale, and what it still delivers is dropped.
	generation int
	// order places live user messages where their turn runs.
	order userOrder
	// usages counts the usage reports the conversation has shown.
	usages int
	// source is the conversation a fork copied, for the ids of its prefix.
	source *assistant.ForkedFrom
	// shown is set once a replay was applied.
	shown bool
}

// bind follows the engine's conversation, stopping any subscription to
// another one. It runs on the actor whenever the conversation may change.
func (e *Engine) bind() {
	id := e.opts.ConversationID
	if e.follower != nil && e.follower.conversation == id {
		return
	}
	if e.follower != nil {
		e.follower.stop()
		e.follower = nil
	}
	e.connection, e.turnState, e.startedCalls = Connection{}, TurnIdle, map[string]bool{}
	backend, ok := e.followable()
	if id == "" || !ok || e.life.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f := &follower{conversation: id, stop: cancel, wake: make(chan struct{}, 1), refresh: make(chan struct{}, 1), source: e.forkedFrom}
	e.follower = f
	e.connection = Connection{State: ConnectionConnecting, Since: time.Now()}
	e.workers.Add(1)
	e.work(ctx, func(ctx context.Context) { e.follow(ctx, backend, f) })
}

// followable returns the backend's conversation subscription, if it has one.
// NOTE: the multi-agent backend has none.
func (e *Engine) followable() (TaskBackend, bool) {
	backend, ok := e.backend.(TaskBackend)
	_, duplex := e.backend.(DuplexBackend)
	return backend, ok && !duplex && !e.sendOnly
}

// Reconnect tries the conversation again at once, without waiting out the
// backoff, when following it failed. A connected subscription is left alone.
func (e *Engine) Reconnect() {
	e.do(e.reconnect)
}

// reconnect is Reconnect on the actor. Sending calls it too: a message is
// worth another try at the conversation.
func (e *Engine) reconnect() {
	if e.follower == nil {
		// A bound conversation is always followed, as after a history load
		// that failed.
		e.bind()
		return
	}
	switch e.connection.State {
	case ConnectionConnecting, ConnectionReconnecting, ConnectionDisconnected, ConnectionNew:
		if f := e.follower; f != nil {
			signal(f.wake)
		}
	case ConnectionIdle, ConnectionConnected:
	}
}

// refreshFollower asks for a fresh replay, as after a local run whose live
// content the subscription did not show.
func (e *Engine) refreshFollower() {
	if f := e.follower; f != nil {
		f.generation++
		signal(f.refresh)
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default: // already pending
	}
}

// follow subscribes to the conversation until ctx ends. Each attempt replays
// the whole conversation into a private transcript, which replaces the
// engine's at the replay's end, then folds live messages. Failed attempts
// back off and never stop while the conversation is bound, so a long outage
// recovers by itself; a stream that closes cleanly after its replay is not a
// failure, since an idle stream may close on its own. Only a conversation
// that refuses to be followed stops the retries, until Reconnect.
func (e *Engine) follow(ctx context.Context, backend TaskBackend, f *follower) {
	failures := 0
	everReplayed := false
	for {
		replayed, err := e.attempt(ctx, backend, f)
		if ctx.Err() != nil {
			return
		}
		everReplayed = everReplayed || replayed
		if errors.Is(err, errRefresh) {
			continue
		}
		if replayed {
			failures = 0
		}
		next, delay := Connection{State: ConnectionConnected}, retryBase
		if err != nil || !replayed {
			failures++
			next, delay = retryAfter(err, failures, everReplayed)
		}
		if next.State == ConnectionDisconnected {
			e.setConnection(f, next)
			select {
			case <-f.wake:
			case <-ctx.Done():
				return
			}
			failures = 0
			e.setConnection(f, Connection{State: ConnectionReconnecting})
			continue
		}
		if next.State != ConnectionConnected {
			e.setConnection(f, next)
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-f.refresh:
			timer.Stop()
		case <-f.wake:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// retryAfter is the state after an attempt failed with err, the failures-th
// failure in a row, and how long to wait before the next one.
func retryAfter(err error, failures int, everReplayed bool) (Connection, time.Duration) {
	apiErr, answered := errors.AsType[*assistant.APIError](err)
	switch {
	case errors.Is(err, auth.ErrReauthRequired):
		// The login cannot be refreshed; retrying cannot help.
		return Connection{State: ConnectionDisconnected, Err: err}, 0
	case !answered:
		// No response, as when the network is down.
		return Connection{State: ConnectionReconnecting, Failures: failures, Err: err}, backoff(failures)
	case apiErr.StatusCode == http.StatusNotFound && !everReplayed:
		// A new conversation exists once its first prompt is accepted.
		return Connection{State: ConnectionNew}, backoff(failures)
	case apiErr.StatusCode == http.StatusNotFound, apiErr.StatusCode == http.StatusForbidden, apiErr.StatusCode == http.StatusBadRequest:
		return Connection{State: ConnectionDisconnected, Err: err}, 0
	}
	// A server asking for a pause gets at least that long. A rejected login
	// is refreshed for the next attempt.
	return Connection{State: ConnectionReconnecting, Failures: failures, Err: err}, max(backoff(failures), apiErr.RetryAfter)
}

// backoff is the wait after failures attempts failed in a row.
func backoff(failures int) time.Duration {
	return min(retryBase<<min(failures-1, 16), retryMax)
}

// attempt runs one subscription and reports whether its replay completed.
func (e *Engine) attempt(ctx context.Context, backend TaskBackend, f *follower) (bool, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case <-f.refresh:
			cancel(errRefresh)
		case <-ctx.Done():
		}
	}()
	type start struct {
		generation int
		source     *assistant.ForkedFrom
	}
	at := get(e, func() start { return start{f.generation, f.source} })
	generation, source := at.generation, at.source
	var messages []assistant.Message
	var order userOrder
	turn := TurnIdle
	replayed := false
	err := backend.Subscribe(ctx, f.conversation, assistant.SubscribeOptions{}, func(msg assistant.Message) error {
		replayComplete := msg.Content.Type == assistant.ContentReplayComplete
		switch {
		case replayComplete && (replayed || msg.Content.Replay == nil || msg.Content.Replay.Kind != assistant.ReplayKindFull):
			return errReplayProtocol
		case replayed:
			e.do(func() { e.applyLive(f, generation, msg) })
		case replayComplete:
			// Folded here, off the actor, since a replay can be long.
			conversation := conversationFromHistory(source, messages)
			replayed = true
			e.do(func() { e.applySnapshot(f, generation, conversation, turn, messages, order) })
		default:
			for _, m := range order.next(msg, func(id string) (int, bool) { return callTurn(messages, id) }) {
				turn = turnAfter(turn, m)
				messages = append(messages, m)
			}
		}
		return nil
	})
	if cause := context.Cause(ctx); errors.Is(cause, errRefresh) {
		return replayed, errRefresh
	}
	return replayed, err
}

// turnAfter is the turn state once msg is seen.
func turnAfter(turn TurnState, msg assistant.Message) TurnState {
	switch {
	case msg.Content.TurnStatus != nil && msg.Content.TurnStatus.Status == "started":
		return TurnRunning
	case msg.Content.TurnStatus != nil && msg.Content.TurnStatus.Status == "ended":
		return TurnIdle
	case msg.Content.Type == assistant.ContentUserStop:
		return TurnStopped
	}
	return turn
}

// setConnection publishes the subscription's state if f is still current.
func (e *Engine) setConnection(f *follower, c Connection) {
	e.do(func() {
		if !e.current(f) {
			return
		}
		c.Since = e.connection.Since
		if c.State != e.connection.State {
			c.Since = time.Now()
		}
		e.connection = c
		e.publishLive()
	})
}

// applySnapshot replaces the transcript with a completed replay, keeping what
// only this process knows about its blocks.
func (e *Engine) applySnapshot(f *follower, generation int, conversation *Conversation, turn TurnState, messages []assistant.Message, order userOrder) {
	if !e.current(f) || f.generation != generation {
		return
	}
	connected := e.connection.State != ConnectionConnected
	e.connection = Connection{State: ConnectionConnected, Since: e.connection.Since}
	if connected {
		e.connection.Since = time.Now()
	}
	order.live = true
	if turn != TurnRunning {
		// The replay shows the messages queued behind no running turn; the
		// turns that start next are theirs.
		order.waiting = queuedAtEnd(messages)
	}
	order.keep(f.order.held, newUserMessages(messages, e.transcript.TurnNumber()))
	f.order = order
	f.shown = true
	next := conversation.transcript
	next.carryLocal(e.transcript, turn == TurnRunning)
	next.keepUnsent(e.transcript)
	next.supersede(e.transcript)
	next.run = e.transcript.run
	prev := e.transcript
	e.transcript = next
	if t := e.turn; t != nil {
		t.claimReplayed(prev, messages)
	}
	if conversation.fork.After != (BlockID{}) {
		e.fork = conversation.fork
	}
	e.turnState = turn
	if e.unsettled != nil {
		e.unsettled()
	}
	// The replay knows which of its tasks finished; only the others are
	// observed.
	var known []*task
	for _, t := range conversation.tasks {
		if findTask(e.tasks, t.ID) == nil {
			known = append(known, t)
		}
	}
	e.addTasks(known)
	e.discoverTasks()
	e.publishLive()
	// Usage the replay reports and this process has not seen, as when the
	// connection dropped as it arrived.
	var usage *assistant.Usage
	usages := 0
	for _, msg := range messages {
		if msg.Results != nil && msg.Results.Usage != nil {
			usage = msg.Results.Usage
			usages++
		}
	}
	if usages > f.usages {
		e.usage = usage
		if e.turn != nil {
			e.turn.emit(Event{Kind: EventUsage, Usage: usage})
		}
	}
	f.usages = usages
	e.confirmFailed()
	if t := e.turn; t != nil {
		t.syncCalls()
	}
	e.offerLeftCalls()
}

// queuedAtEnd counts the user messages after the last content of a
// conversation with no turn running: they wait for their turns. Answers to
// one turn's calls are one message.
func queuedAtEnd(messages []assistant.Message) int {
	n := 0
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Content.TurnStatus != nil {
			continue
		}
		if assistant.RoleOf(m.Role) != assistant.RoleUser || m.Content.Type == assistant.ContentUserStop {
			break
		}
		if !isAnswer(m) || i == 0 || !isAnswer(messages[i-1]) {
			n++
		}
	}
	return n
}

// newUserMessages are the user messages of messages after the first shown.
func newUserMessages(messages []assistant.Message, shown int) []assistant.Message {
	var out []assistant.Message
	for _, m := range messages {
		if assistant.RoleOf(m.Role) != assistant.RoleUser || m.Content.Type == assistant.ContentUserStop {
			continue
		}
		if shown > 0 {
			shown--
			continue
		}
		out = append(out, m)
	}
	return out
}

// applyLive folds one live message of the conversation.
func (e *Engine) applyLive(f *follower, generation int, msg assistant.Message) {
	if !e.current(f) || f.generation != generation || msg.BackgroundTaskID != nil {
		return
	}
	if msg.Results != nil && msg.Results.Usage != nil {
		f.usages++
	}
	tr := e.transcript
	for _, m := range f.order.next(msg, func(id string) (int, bool) {
		i, ok := tr.index[BlockID{Scope: ScopeTool, Key: id}]
		if !ok {
			return 0, false
		}
		return tr.blocks[i].TurnNumber, true
	}) {
		e.foldLive(m)
	}
}

// foldLive folds one live message, in conversation order.
func (e *Engine) foldLive(msg assistant.Message) {
	// A turn that ended, or was stopped, streams nothing more: its text is
	// complete, whichever session ran it.
	ended := msg.Content.Type == assistant.ContentUserStop || msg.Content.TurnStatus != nil && msg.Content.TurnStatus.Status == "ended"
	if ended {
		e.transcript.FinalizeAll()
	}
	switch {
	case msg.Content.TurnStatus != nil && msg.Content.TurnStatus.Status == "started":
	case assistant.RoleOf(msg.Role) == assistant.RoleUser:
		if t := e.turn; t != nil && t.stopRequested && msg.Content.Type == assistant.ContentClientToolResponse {
			// The answer to a call the stop hid stays hidden too.
			e.transcript.noteAnswer(msg)
		} else {
			e.transcript.AppendMessage(msg)
		}
		e.confirmFailed()
		// The turn start that released it follows.
		return
	case e.turn != nil:
		// The local run shows it, with its own tools and events. A stop
		// hides content.
		t := e.turn
		e.turnState = turnAfter(e.turnState, msg)

		if !t.stopRequested || msg.IsError || t.knows(msg) {
			// A failure still shows, as does a call the stop answered: it
			// ran as it arrived, before the conversation showed it.
			t.fold(msg)
		}
		e.discoverTasks()
		t.syncCalls()
		if ended {
			t.publish()
		}
		return
	default:
		e.transcript.AppendMessage(msg)
		if msg.Results != nil && msg.Results.Usage != nil {
			e.usage = msg.Results.Usage
		}
	}
	e.turnState = turnAfter(e.turnState, msg)
	e.discoverTasks()
	e.publishLive()
	if e.turn != nil {
		e.turn.syncCalls()
	}
	e.offerLeftCalls()
}

// userOrder places user messages where their turn runs. The conversation
// shows a message as soon as it is queued, which may be while another turn
// runs; the message belongs to the turn that starts for it. So each user
// message waits, and each turn that starts takes the next waiting message,
// with the answers queued right after it when it is an answer. Placement is
// only for display: a wrong guess is corrected by the next replay.
//
// A replay's saved part has no turn markers and is already in order, so
// messages wait only from the first marker on: from there a replay shows
// what the conversation showed live.
type userOrder struct {
	live bool
	held []assistant.Message
	// released is set by a turn start with no message waiting.
	released bool
	// ended is set from a turn's end to the next turn's start.
	ended bool
	// waiting counts the messages a replay showed queued, whose turns have
	// not started.
	waiting int
}

// take releases the next waiting message: the answers of one request,
// those to calls of the same turn, are one turn's message. turnOf is the
// turn of a call.
func (o *userOrder) take(turnOf func(string) (int, bool)) []assistant.Message {
	n := 1
	for n < len(o.held) && isAnswer(o.held[0]) && isAnswer(o.held[n]) && sameTurn(turnOf, o.held[0], o.held[n]) {
		n++
	}
	if isAnswer(o.held[0]) && n < len(o.held) && !isAnswer(o.held[n]) {
		// Answers sent with a prompt, as denials after a stop, are one
		// request: the prompt runs in the same turn.
		n++
	}
	out := slices.Clone(o.held[:n])
	o.held = slices.Clone(o.held[n:])
	return out
}

// next returns what seeing msg releases, in conversation order.
func (o *userOrder) next(msg assistant.Message, turnOf func(string) (int, bool)) []assistant.Message {
	first := !o.live && msg.Content.TurnStatus != nil
	o.live = o.live || msg.Content.TurnStatus != nil
	switch {
	case !o.live:
	case msg.Content.TurnStatus != nil && msg.Content.TurnStatus.Status == "ended":
		o.ended = true
	case msg.Content.TurnStatus != nil && msg.Content.TurnStatus.Status == "started":
		o.ended = false
		if len(o.held) == 0 && o.waiting > 0 {
			// Its message was shown queued by the replay.
			o.waiting--
			return []assistant.Message{msg}
		}
		if len(o.held) == 0 {
			// Its message was never seen, or the marker of an earlier turn
			// was missed: the next message is this turn's. The first
			// marker follows its message, already shown in order.
			o.released = !first
			return []assistant.Message{msg}
		}
		return append(o.take(turnOf), msg)
	case o.ended && len(o.held) > 0 && assistant.RoleOf(msg.Role) != assistant.RoleUser:
		// Content after a turn ended is the next turn's, whose start
		// marker was missed.
		o.ended = false
		return append(o.take(turnOf), msg)
	case assistant.RoleOf(msg.Role) == assistant.RoleUser && msg.Content.Type != assistant.ContentUserStop:
		if o.released {
			o.released = false
			return []assistant.Message{msg}
		}
		o.held = append(o.held, msg)
		return nil
	}
	return []assistant.Message{msg}
}

// sameTurn reports whether answers a and b answer calls of one turn.
func sameTurn(turnOf func(string) (int, bool), a, b assistant.Message) bool {
	x, okA := turnOf(a.Content.Tool.ToolCallID)
	y, okB := turnOf(b.Content.Tool.ToolCallID)
	return okA && okB && x == y
}

// callTurn is the turn of the call id in messages: the user messages
// before it.
func callTurn(messages []assistant.Message, id string) (int, bool) {
	n := 0
	for _, m := range messages {
		switch {
		case assistant.RoleOf(m.Role) == assistant.RoleUser && m.Content.Type != assistant.ContentUserStop:
			n++
		case m.Content.Tool != nil && m.Content.Tool.ToolCallID == id && m.Content.Type == assistant.ContentClientToolCall:
			return n, true
		}
	}
	return 0, false
}

func isAnswer(m assistant.Message) bool { return m.Content.Type == assistant.ContentClientToolResponse }

// keep adds the messages of held that messages, the user messages a replay
// shows beyond what was shown before, does not contain: their turn has not
// started yet, though the replay may not show them waiting.
func (o *userOrder) keep(held, messages []assistant.Message) {
	shown := map[string]int{}
	for _, m := range slices.Concat(messages, o.held) {
		shown[userKey(m)]++
	}
	for _, m := range held {
		if k := userKey(m); shown[k] > 0 {
			shown[k]--
			continue
		}
		o.held = append(o.held, m)
	}
}

// userKey names a user message across copies, which have different ids.
func userKey(m assistant.Message) string {
	switch {
	case m.Content.Tool != nil:
		return "answer:" + m.Content.Tool.ToolCallID
	case m.Content.Markdown != nil:
		return "prompt:" + m.Content.Markdown.Content
	}
	return m.MessageID
}

// claimReplayed gives the local run what a replay showed of its turn and
// the run did not see live, as after a dropped connection: the run's blocks
// keep its run id, and its tool messages get the client-side marking and
// input reducers the live path gives them, so render state and approvals
// survive a reconnect.
func (t *turn) claimReplayed(prev *Transcript, messages []assistant.Message) {
	tr := t.e.transcript
	ours := func(id BlockID) (int, bool) {
		i, ok := tr.index[id]
		if !ok {
			return 0, false
		}
		j, seen := prev.index[id]
		return i, !seen || prev.blocks[j].Run == tr.run
	}
	for i, b := range tr.blocks {
		if _, mine := ours(b.ID); mine && b.Role != assistant.RoleUser {
			tr.blocks[i].Run = tr.run
		}
	}
	for _, msg := range messages {
		if msg.Content.Tool == nil || assistant.RoleOf(msg.Role) == assistant.RoleUser {
			continue
		}
		i, mine := ours(BlockIDOf(msg))
		if !mine {
			continue
		}
		if msg.Content.Type == assistant.ContentToolCallStarted && !msg.Content.Tool.HasClientSide && t.tools.Has(msg.Content.Tool.ToolName) {
			tr.MarkToolClientSide(msg.Content.Tool.ToolCallID)
		}
		t.e.reduceToolInput(t.ctx, t.tools, msg, tr.blocks[i])
	}
}

// current reports whether f still follows for a live engine: once Close
// begins, the subscription changes nothing the closing operation reported.
func (e *Engine) current(f *follower) bool {
	return e.follower == f && e.life.Err() == nil
}

// publishLive publishes a change the subscription made: as the local run's,
// if one is running, so it reaches the run's followers in order.
func (e *Engine) publishLive() {
	if e.turn != nil {
		e.turn.publish()
		return
	}
	e.publish()
}

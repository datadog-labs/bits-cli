package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
)

// liveBackend serves the conversation subscription: attempt i streams
// attempts[i] then ends with its error; past the last one it stays open.
type liveBackend struct {
	scriptBackend
	attempts []liveAttempt

	mu sync.Mutex
	n  int
}

type liveAttempt struct {
	// delay passes before the attempt's messages arrive.
	delay    time.Duration
	messages []assistant.Message
	err      error
}

func (b *liveBackend) Subscribe(ctx context.Context, _ string, opts assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	b.mu.Lock()
	i := b.n
	if opts.TaskID == "" {
		b.n++
	}
	b.mu.Unlock()
	if opts.TaskID != "" || i >= len(b.attempts) {
		<-ctx.Done()
		return ctx.Err()
	}
	time.Sleep(b.attempts[i].delay)
	for _, msg := range b.attempts[i].messages {
		if err := fn(msg); err != nil {
			return err
		}
	}
	return b.attempts[i].err
}

func (b *liveBackend) subscriptions() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

func userMsg(id, text string) assistant.Message {
	return assistant.Message{Role: "user", MessageID: id, Content: assistant.TextContent(text)}
}

func statusMsg(status string) assistant.Message {
	return assistant.AssistantMessage("marker-"+status, assistant.Content{
		Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: status},
	})
}

func statusErr(code int) error { return &assistant.APIError{StatusCode: code} }

// turnTexts lists the transcript's text blocks as "turn:text".
func turnTexts(blocks []Block) []string {
	var out []string
	for _, b := range blocks {
		if b.Markdown != nil {
			out = append(out, string(rune('0'+b.TurnNumber))+":"+b.Markdown.Content)
		}
	}
	return out
}

func TestFollowConversation(t *testing.T) {
	saved := []assistant.Message{userMsg("u1", "one"), textMsg("a1", "first")}
	replay := append(slices.Clone(saved), replayComplete)
	taskUpdate := textMsg("t1", "task output")
	taskUpdate.BackgroundTaskID = new(string)
	for _, test := range []struct {
		name       string
		attempts   []liveAttempt
		texts      []string
		turn       TurnState
		connection ConnectionState
		failures   int
	}{
		{
			name: "replay then live",
			attempts: []liveAttempt{{messages: append(slices.Clone(replay),
				userMsg("echo-2", "two"), statusMsg("started"), textMsg("a2", "second"), statusMsg("ended"))}},
			texts: []string{"1:one", "1:first", "2:two", "2:second"}, turn: TurnIdle, connection: ConnectionConnected,
		},
		{
			name:     "a turn running elsewhere",
			attempts: []liveAttempt{{messages: append(slices.Clone(saved), statusMsg("started"), textMsg("a2", "partial"), replayComplete)}},
			texts:    []string{"1:one", "1:first", "1:partial"}, turn: TurnRunning, connection: ConnectionConnected,
		},
		{
			name: "a prompt queued during a turn shows after it",
			attempts: []liveAttempt{{messages: []assistant.Message{
				userMsg("u1", "one"), statusMsg("started"), replayComplete,
				userMsg("echo-2", "two"), textMsg("a1", "first"), statusMsg("ended"),
				statusMsg("started"), textMsg("a2", "second"),
			}}},
			texts: []string{"1:one", "1:first", "2:two", "2:second"}, turn: TurnRunning, connection: ConnectionConnected,
		},
		{
			name: "each turn start takes one queued prompt",
			attempts: []liveAttempt{{messages: []assistant.Message{
				userMsg("u1", "one"), statusMsg("started"), replayComplete,
				userMsg("echo-2", "two"), userMsg("echo-3", "three"), textMsg("a1", "first"), statusMsg("ended"),
				statusMsg("started"), textMsg("a2", "second"),
			}}},
			texts: []string{"1:one", "1:first", "2:two", "2:second"}, turn: TurnRunning, connection: ConnectionConnected,
		},
		{
			name: "queued prompts keep their place across a reconnect",
			attempts: []liveAttempt{
				{messages: []assistant.Message{userMsg("u1", "one"), statusMsg("started"), userMsg("echo-2", "two"), userMsg("echo-3", "three"), textMsg("a1", "partial"), replayComplete}, err: errors.New("reset")},
				{messages: []assistant.Message{
					userMsg("u1", "one"), statusMsg("started"), userMsg("echo-2", "two"), userMsg("echo-3", "three"), textMsg("a1", "first"), replayComplete,
					statusMsg("ended"), statusMsg("started"), textMsg("a2", "second"), statusMsg("ended"), statusMsg("started"),
				}},
			},
			texts: []string{"1:one", "1:first", "2:two", "2:second", "3:three"}, turn: TurnRunning, connection: ConnectionConnected,
		},
		{
			name: "a missed turn start still shows the prompt first",
			attempts: []liveAttempt{{messages: []assistant.Message{
				userMsg("u1", "one"), statusMsg("started"), replayComplete,
				userMsg("echo-2", "two"), textMsg("a1", "first"), statusMsg("ended"),
				textMsg("a2", "second"),
			}}},
			texts: []string{"1:one", "1:first", "2:two", "2:second"}, turn: TurnIdle, connection: ConnectionConnected,
		},
		{
			name:     "task messages are left to the task",
			attempts: []liveAttempt{{messages: append(slices.Clone(replay), taskUpdate)}},
			texts:    []string{"1:one", "1:first"}, connection: ConnectionConnected,
		},
		{
			name:     "a stream that ends before its replay keeps the snapshot",
			attempts: []liveAttempt{{messages: replay}, {messages: saved[:1], err: errors.New("reset")}},
			texts:    []string{"1:one", "1:first"}, connection: ConnectionReconnecting, failures: 1,
		},
		{
			name:     "a second replay marker is a failure",
			attempts: []liveAttempt{{messages: append(slices.Clone(replay), replayComplete)}},
			texts:    []string{"1:one", "1:first"}, connection: ConnectionReconnecting, failures: 1,
		},
		{
			name: "a replay of an unknown kind is a failure",
			attempts: []liveAttempt{{messages: append(slices.Clone(saved), assistant.AssistantMessage(assistant.ContentReplayComplete,
				assistant.Content{Type: assistant.ContentReplayComplete, Replay: &assistant.ReplayPayload{Kind: "partial"}}))}},
			connection: ConnectionReconnecting, failures: 1,
		},
		{
			name:       "not found before any replay keeps waiting",
			attempts:   []liveAttempt{{err: statusErr(http.StatusNotFound)}},
			connection: ConnectionNew,
		},
		{
			name:       "not found after a replay disconnects",
			attempts:   []liveAttempt{{messages: replay, err: errors.New("reset")}, {err: statusErr(http.StatusNotFound)}},
			texts:      []string{"1:one", "1:first"},
			connection: ConnectionDisconnected,
		},
		{
			name:       "forbidden disconnects",
			attempts:   []liveAttempt{{err: statusErr(http.StatusForbidden)}},
			connection: ConnectionDisconnected,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &liveBackend{attempts: test.attempts}
				e := following(backend)
				defer e.Close()
				// Retries wait on the bubble's clock: an hour outlasts them all.
				time.Sleep(time.Hour)
				synctest.Wait()
				state := e.State()
				if got := turnTexts(state.Blocks); !slices.Equal(got, test.texts) {
					t.Errorf("texts = %q, want %q", got, test.texts)
				}
				if state.Turn != test.turn || state.Connection.State != test.connection || state.Connection.Failures != test.failures {
					t.Errorf("turn = %v, connection = %+v; want %v, %v with %d failures", state.Turn, state.Connection, test.turn, test.connection, test.failures)
				}
			})
		})
	}
}

// A prompt queued during a turn waits for its turn to start; a reconnect's
// replay that does not show it waiting keeps it, so its turn still shows it.
func TestSwapKeepsHeldPrompts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &liveBackend{attempts: []liveAttempt{
			{messages: []assistant.Message{userMsg("u1", "one"), statusMsg("started"), replayComplete, userMsg("echo-2", "two")}, err: errors.New("reset")},
			{messages: []assistant.Message{
				userMsg("u1", "one"), statusMsg("started"), textMsg("a1", "first"), replayComplete,
				statusMsg("ended"), statusMsg("started"), textMsg("a2", "second"),
			}},
		}}
		e := following(backend)
		defer e.Close()
		time.Sleep(time.Hour)
		synctest.Wait()
		want := []string{"1:one", "1:first", "2:two", "2:second"}
		if got := turnTexts(e.State().Blocks); !slices.Equal(got, want) {
			t.Fatalf("texts = %q, want %q", got, want)
		}
	})
}

// A conversation that keeps failing is retried without end, each wait
// doubling from a second up to 30 seconds, and recovers when the network
// returns.
func TestFollowRecoversWhenTheNetworkReturns(t *testing.T) {
	offline := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"network", offline},
		{"server", statusErr(http.StatusServiceUnavailable)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const failing = 40 // far past any count that once stopped retries
				attempts := make([]liveAttempt, failing)
				for i := range attempts {
					attempts[i].err = tc.err
				}
				backend := &liveBackend{attempts: append(attempts, liveAttempt{messages: []assistant.Message{userMsg("u1", "one"), replayComplete}})}
				e := following(backend)
				defer e.Close()
				synctest.Wait()
				for n := 1; n < failing; n++ {
					wait := min(time.Second<<min(n-1, 16), 30*time.Second)
					time.Sleep(wait - time.Millisecond)
					synctest.Wait()
					if c := e.State().Connection; c.State != ConnectionReconnecting || c.Failures != n {
						t.Fatalf("after %d failures and %v: connection = %+v, want still waiting", n, wait, c)
					}
					time.Sleep(time.Millisecond)
					synctest.Wait()
				}
				time.Sleep(30 * time.Second)
				synctest.Wait()
				if c := e.State().Connection; c.State != ConnectionConnected || len(e.State().Blocks) != 1 {
					t.Fatalf("connection = %+v, blocks = %d; want recovered", c, len(e.State().Blocks))
				}
			})
		})
	}
}

// Reconnecting and sending try again at once, without waiting out the backoff.
func TestReconnectSkipsTheWait(t *testing.T) {
	for _, resume := range []struct {
		name string
		run  func(*Engine) error
	}{
		{"Reconnect", func(e *Engine) error { e.Reconnect(); return nil }},
		{"Queue", func(e *Engine) error { return e.Queue(TurnInput{Message: "later"}) }},
	} {
		t.Run(resume.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &liveBackend{attempts: []liveAttempt{
					{err: statusErr(http.StatusServiceUnavailable)},
					{messages: []assistant.Message{userMsg("u1", "one"), replayComplete}},
				}}
				e := following(backend)
				defer e.Close()
				synctest.Wait()
				if c := e.State().Connection; c.State != ConnectionReconnecting {
					t.Fatalf("connection = %+v, want reconnecting", c)
				}
				if err := resume.run(e); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if c := e.State().Connection; c.State != ConnectionConnected || len(e.State().Blocks) != 1 {
					t.Fatalf("connection = %+v, blocks = %d; want connected with no wait", c, len(e.State().Blocks))
				}
			})
		})
	}
}

// Only a conversation that refuses to be followed stops the retries.
func TestFollowStopsOnlyWhenRefused(t *testing.T) {
	replay := liveAttempt{messages: []assistant.Message{userMsg("u1", "one"), replayComplete}, err: errors.New("closed")}
	for _, tc := range []struct {
		name     string
		attempts []liveAttempt
		terminal bool
	}{
		{"forbidden", []liveAttempt{{err: statusErr(http.StatusForbidden)}}, true},
		{"bad request", []liveAttempt{{err: statusErr(http.StatusBadRequest)}}, true},
		{"not found after a replay", []liveAttempt{replay, {err: statusErr(http.StatusNotFound)}}, true},
		{"login expired", []liveAttempt{{err: fmt.Errorf("token: %w", auth.ErrReauthRequired)}}, true},
		{"not found before a replay", []liveAttempt{{err: statusErr(http.StatusNotFound)}}, false},
		{"unauthorized", []liveAttempt{{err: statusErr(http.StatusUnauthorized)}}, false},
		{"rate limited", []liveAttempt{{err: statusErr(http.StatusTooManyRequests)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				backend := &liveBackend{attempts: tc.attempts}
				e := following(backend)
				defer e.Close()
				time.Sleep(2 * time.Second)
				synctest.Wait()
				c := e.State().Connection
				if terminal := c.State == ConnectionDisconnected; terminal != tc.terminal || (terminal && c.Err == nil) {
					t.Fatalf("connection = %+v, want terminal = %v with its reason", c, tc.terminal)
				}
			})
		})
	}
}

// A subscription the server rate-limits waits as long as it asks.
func TestFollowWaitsForRetryAfter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limited := &assistant.APIError{StatusCode: http.StatusTooManyRequests, RetryAfter: 45 * time.Second}
		backend := &liveBackend{attempts: []liveAttempt{{err: limited}, {messages: []assistant.Message{userMsg("u1", "one"), replayComplete}}}}
		e := following(backend)
		defer e.Close()
		time.Sleep(44 * time.Second)
		synctest.Wait()
		if n := backend.subscriptions(); n != 1 {
			t.Fatalf("subscriptions after 44s = %d, want 1", n)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if n, c := backend.subscriptions(), e.State().Connection; n < 2 || c.State != ConnectionConnected {
			t.Fatalf("after 46s: subscriptions = %d, connection = %+v", n, c)
		}
	})
}

// A replay keeps block ids and what only this process knows about them.
func TestReplayKeepsLocalToolState(t *testing.T) {
	call := clientCallMsg("call-1", "read", "{}")
	prev := NewTranscript()
	prev.AppendMessage(userMsg("u1", "one"))
	prev.AppendMessage(call)
	prev.markTool("call-1", func(tool *ToolBlock) { tool.Status, tool.RenderState = ToolDenied, "shown" })

	next := conversationFromHistory(nil, []assistant.Message{userMsg("u1", "one"), call}).transcript
	next.carryLocal(prev, false)
	got, want := next.Blocks(), prev.Blocks()
	if len(got) != 2 || got[0].ID != want[0].ID || got[1].ID != want[1].ID {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if tool := got[1].Tool; tool.Status != ToolDenied || tool.RenderState != "shown" {
		t.Fatalf("tool = %+v, want the local outcome and render state", tool)
	}
}

// ownershipBackend streams each send's frames on the subscription, as the
// conversation shows them, and delivers on the send's own response only the
// calls of each send. A frame named "drop" breaks
// the subscription, whose next attempt replays everything shown so far.
type ownershipBackend struct {
	sends [][]assistant.Message
	// elsewhere is another session's turn, shown on the subscription after
	// the first send's frames and never on this client's responses.
	elsewhere []assistant.Message
	frames    chan assistant.Message
	n         atomic.Int32

	mu      sync.Mutex
	log     []assistant.Message
	answers []string // the call ids every send answered
}

func newOwnershipBackend(sends ...[]assistant.Message) *ownershipBackend {
	return &ownershipBackend{sends: sends, frames: make(chan assistant.Message, 256)}
}

func (b *ownershipBackend) Subscribe(ctx context.Context, id string, o assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	if o.TaskID != "" {
		<-ctx.Done()
		return ctx.Err()
	}
	b.mu.Lock()
	replay := append(slices.Clone(b.log), replayComplete)
	b.mu.Unlock()
	for _, m := range replay {
		if err := fn(m); err != nil {
			return err
		}
	}
	for {
		select {
		case m := <-b.frames:
			if m.MessageID == "drop" {
				return errors.New("connection reset")
			}
			b.mu.Lock()
			b.log = append(b.log, m)
			b.mu.Unlock()
			if err := fn(m); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *ownershipBackend) Send(ctx context.Context, message any, o assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	if rs, ok := message.([]assistant.ClientToolResponse); ok {
		b.mu.Lock()
		for _, r := range rs {
			b.answers = append(b.answers, r.ToolCallID)
		}
		b.mu.Unlock()
	}
	if n := int(b.n.Add(1)) - 1; n < len(b.sends) {
		for _, m := range b.sends[n] {
			b.frames <- m
			if m.Content.Type == assistant.ContentClientToolCall {
				var r assistant.AssistantResponse
				r.Data.Attributes.StructuredMessage = m
				if err := fn(r); err != nil {
					return o.ConversationID, err
				}
			}
		}
		if n == 0 {
			for _, m := range b.elsewhere {
				b.frames <- m
			}
		}
	}
	// The response ends with the turn, paused or done.
	return o.ConversationID, nil
}

func answerMsg(id string) assistant.Message {
	return assistant.Message{Role: "user", MessageID: "answer-" + id, Content: assistant.Content{
		Type: assistant.ContentClientToolResponse,
		Tool: &assistant.ToolPayload{ToolCallID: id, Status: "success"},
	}}
}

// Every call that arrives on this client's own send runs here, once per
// id, whichever session's turn made it, as in the web client: the send's
// response is this client's to answer. A call shown again, by a reconnect's
// replay or a repeated delivery, never runs twice.
func TestRunsEachCallFromOwnSendOnce(t *testing.T) {
	started, ended := statusMsg("started"), statusMsg("ended")
	cases := []struct {
		name  string
		sends [][]assistant.Message
		want  map[string]int
	}{
		{"a call of our turn", [][]assistant.Message{
			{userMsg("local-u", "mine"), started, clientCallMsg("c1", "first", "{}"), ended},
			{answerMsg("c1"), started, textMsg("end", "done"), ended},
		}, map[string]int{"c1": 1}},
		{"a reconnect shows our call again", [][]assistant.Message{
			{userMsg("local-u", "mine"), started, clientCallMsg("c1", "first", "{}"), ended},
			{answerMsg("c1"), assistant.Message{MessageID: "drop"}, started, textMsg("end", "done"), ended},
		}, map[string]int{"c1": 1}},
		{"a call delivered twice", [][]assistant.Message{
			{userMsg("local-u", "mine"), started, clientCallMsg("c1", "first", "{}"), clientCallMsg("c1", "first", "{}"), ended},
			{answerMsg("c1"), started, textMsg("end", "done"), ended},
		}, map[string]int{"c1": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newOwnershipBackend(tc.sends...)
				runs := map[string]int{}
				tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "first"}, Handler: func(_ context.Context, c ToolCall) (ToolResult, error) {
					runs[c.ID]++
					return ToolResult{Output: "ok"}, nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				e := New(b, Config{ConversationID: testConversationID, PermissionsMode: ModeSkipPermissions})
				defer e.Close()
				events := drain(t, e, TurnInput{Message: "mine", Tools: tools})
				if last := events[len(events)-1].Kind; !maps.Equal(runs, tc.want) || last != EventDone {
					t.Fatalf("runs = %v (want %v), last = %v", runs, tc.want, last)
				}
			})
		})
	}
}

// A prompt waits while the conversation shows a client call nobody has
// answered, whichever session made it, and while this client's own pause
// is answered; it goes out once nothing holds it.
func TestSendWaitsForPendingClientCalls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, ended := statusMsg("started"), statusMsg("ended")
		b := newOwnershipBackend()
		e := New(b, Config{ConversationID: testConversationID, PermissionsMode: ModeSkipPermissions})
		defer e.Close()
		e.do(e.bind)
		synctest.Wait()
		for _, m := range []assistant.Message{userMsg("remote-u", "remote"), started, clientCallMsg("remote-a", "first", "{}"), ended} {
			b.frames <- m
		}
		synctest.Wait()
		if _, err := e.StartTurn(context.Background(), TurnInput{Message: "mine"}); !errors.Is(err, ErrSendBlocked) {
			t.Fatalf("StartTurn = %v, want the prompt held", err)
		}
		if _, err := e.SendQueued(context.Background(), TurnInput{}); !errors.Is(err, ErrSendBlocked) {
			t.Fatalf("SendQueued = %v, want the queue held", err)
		}
		b.frames <- answerMsg("remote-a")
		b.frames <- started
		b.frames <- ended
		synctest.Wait()
		if SendBlocked(e.State()) || len(e.State().Queued) != 1 {
			t.Fatalf("state = %+v, want the prompt queued and free to go", e.State())
		}
		if err := e.NewConversation(); err != nil {
			t.Fatalf("NewConversation = %v; a new conversation is never held", err)
		}
	})
}

// During this client's own pause the queue waits for the continuation's
// turn to end, and the continuation itself is never held.
func TestQueueWaitsForOwnContinuation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, ended := statusMsg("started"), statusMsg("ended")
		b := newOwnershipBackend(
			[]assistant.Message{userMsg("local-u", "mine"), started, clientCallMsg("my-c", "first", "{}"), ended},
			[]assistant.Message{answerMsg("my-c"), started, textMsg("my-end", "done"), ended},
			[]assistant.Message{userMsg("local-q", "queued"), started, textMsg("q-end", "queued done"), ended},
		)
		release := make(chan struct{})
		tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "first"}, Handler: func(context.Context, ToolCall) (ToolResult, error) {
			<-release
			return ToolResult{Output: "ok"}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		e := New(b, Config{ConversationID: testConversationID, PermissionsMode: ModeSkipPermissions})
		defer e.Close()
		events, unsub := e.Subscribe()
		defer unsub()
		run, err := e.StartTurn(context.Background(), TurnInput{Message: "mine", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if err := e.Queue(TurnInput{Message: "queued"}); err != nil {
			t.Fatal(err)
		}
		if n := b.n.Load(); n != 1 {
			t.Fatalf("sends during the pause = %d, want only the prompt", n)
		}
		close(release)
		for range Follow(events, run) {
		}
		// The run ends with its response; the queue goes once the
		// conversation shows the turn ended, if the run did not send it.
		if len(e.State().Queued) > 0 {
			awaitFree(t, e)
			collect(follow(e, func() (RunID, error) { return e.SendQueued(t.Context(), TurnInput{}) }))
		}
		if n := b.n.Load(); n != 3 {
			t.Fatalf("sends = %d, want the prompt, its answers, then the queued prompt", n)
		}
	})
}

// A history load that fails still leaves the conversation followed.
type offlineRestoreBackend struct{ liveBackend }

func (b *offlineRestoreBackend) ConversationHistory(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}
}

func TestRestoreFailureStillFollows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &offlineRestoreBackend{liveBackend{attempts: []liveAttempt{{messages: []assistant.Message{
			userMsg("remote", "remote"), statusMsg("started"), textMsg("a", "working"), replayComplete,
		}}}}}
		e := New(b, Config{ConversationID: testConversationID})
		defer e.Close()
		events, unsub := e.Subscribe()
		defer unsub()
		run, err := e.Restore(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for range Follow(events, run) {
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if s := e.State(); b.subscriptions() == 0 || s.Turn != TurnRunning {
			t.Fatalf("subscriptions = %d, turn = %v; want the bound conversation followed", b.subscriptions(), s.Turn)
		}
	})
}

type restoreBackend struct {
	liveBackend
	history  []assistant.Message
	forkedOf *assistant.ForkedFrom
}

func (b *restoreBackend) ConversationHistory(ctx context.Context, in assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	r := &assistant.ConversationHistoryResponse{}
	r.Data.Type = "conversation-history-response"
	r.Data.Attributes.Messages = b.history
	r.Data.Attributes.ForkedFrom = b.forkedOf
	return r, nil
}

// Following starts once Restore installed history, so the replay's newer
// content replaces it, never the other way round.
func TestRestoreKeepsNewerSubscriptionContent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		replay := []assistant.Message{userMsg("u", "one"), statusMsg("started"), textMsg("a", "partial"), replayComplete, textMsg("live-new", "newer")}
		b := &restoreBackend{liveBackend: liveBackend{attempts: []liveAttempt{{messages: replay}, {messages: replay}}}, history: []assistant.Message{userMsg("u", "one")}}
		e := New(b, Config{ConversationID: testConversationID})
		defer e.Close()
		synctest.Wait()
		events, unsub := e.Subscribe()
		defer unsub()
		run, err := e.Restore(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for range Follow(events, run) {
		}
		synctest.Wait()
		if got := e.State().Blocks; len(got) != 3 {
			t.Fatalf("blocks = %v, want the subscription's newer content", turnTexts(got))
		}
	})
}

// A restored fork keeps its copied prefix's ids across a reconnect.
func TestRestoredForkKeepsPrefixOnReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		history := []assistant.Message{userMsg("prefix", "before"), textMsg("fork", "answer"), userMsg("local", "after")}
		replay := append(slices.Clone(history), replayComplete)
		b := &restoreBackend{
			liveBackend: liveBackend{attempts: []liveAttempt{{messages: replay}, {messages: replay}, {messages: replay}}},
			history:     history,
			forkedOf:    &assistant.ForkedFrom{ConversationID: "87654321-4321-4321-4321-210987654321", ForkMessageID: "fork"},
		}
		e := New(b, Config{ConversationID: testConversationID})
		defer e.Close()
		synctest.Wait()
		events, unsub := e.Subscribe()
		defer unsub()
		run, err := e.Restore(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for range Follow(events, run) {
		}
		synctest.Wait()
		fork := e.State().Fork
		time.Sleep(2 * time.Second) // the stream closes; the engine reconnects
		synctest.Wait()
		s := e.State()
		if s.Blocks[0].ID.Scope != ScopeCopied || s.Fork != fork || fork.After == (BlockID{}) {
			t.Fatalf("after reconnect id = %v, fork = %+v (was %+v)", s.Blocks[0].ID, s.Fork, fork)
		}
	})
}

// following returns an engine following the test conversation, as it does
// once a turn or restore binds it.
func following(b Backend) *Engine {
	e := New(b, Config{ConversationID: testConversationID})
	e.do(e.bind)
	return e
}

func TestForeignCallStaysPendingAfterLocalRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := newOwnershipBackend([]assistant.Message{userMsg("local", "mine"), statusMsg("started"), clientCallMsg("my-call", "first", "{}"), statusMsg("ended")}, []assistant.Message{answerMsg("my-call"), statusMsg("started"), textMsg("done", "done"), statusMsg("ended")})
		b.elsewhere = []assistant.Message{userMsg("remote", "remote"), statusMsg("started"), clientCallMsg("remote-call", "first", "{}"), statusMsg("ended")}
		release := make(chan struct{})
		tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "first"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { <-release; return ToolResult{Output: "ok"}, nil }})
		if err != nil {
			t.Fatal(err)
		}
		e := New(b, Config{ConversationID: testConversationID, PermissionsMode: ModeSkipPermissions})
		defer e.Close()
		events, unsub := e.Subscribe()
		defer unsub()
		run, err := e.StartTurn(context.Background(), TurnInput{Message: "mine", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		close(release)
		for range Follow(events, run) {
		}
		synctest.Wait()
		s := e.State()
		if !SendBlocked(s) {
			t.Fatalf("foreign call remains unanswered but gate is open: pending=%v", s.PendingClientCalls())
		}
	})
}

type twinPromptBackend struct{ ownershipBackend }

func (b *twinPromptBackend) Send(ctx context.Context, m any, o assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	// None of these foreign calls was delivered on our response. Break our
	// send after the subscription sees the whole conversation, so the dropped
	// request's manual recovery path is exercised.
	for _, frame := range b.sends[0] {
		b.frames <- frame
	}
	return o.ConversationID, assistant.ErrStreamBroken
}

// After a dropped send, the calls the conversation shows unanswered may be
// anyone's: they never run by themselves, and the manual mode offers them
// for an explicit approval.
func TestBrokenSendOffersPendingCallsForApproval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &twinPromptBackend{*newOwnershipBackend([]assistant.Message{userMsg("r", "mine"), userMsg("l", "mine"), statusMsg("started"), clientCallMsg("foreign-call", "foreign", "{}"), statusMsg("ended"), statusMsg("started"), textMsg("my-answer", "done"), statusMsg("ended")})}
		var ran atomic.Int32
		tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "foreign"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { ran.Add(1); return ToolResult{Output: "ok"}, nil }})
		if err != nil {
			t.Fatal(err)
		}
		e := New(b, Config{ConversationID: testConversationID, PermissionsMode: ModeManual})
		defer e.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err = e.StartTurn(ctx, TurnInput{Message: "mine", Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		offered := false
		for _, b := range e.State().Blocks {
			offered = offered || b.Tool != nil && b.ToolCallID() == "foreign-call" && b.Tool.Status == ToolAwaitingApproval
		}
		if !offered || ran.Load() != 0 {
			t.Fatalf("offered = %t, ran = %d; want the call offered and not run", offered, ran.Load())
		}
	})
}

type outageBackend struct {
	ownershipBackend
	sub atomic.Int32
}

func (b *outageBackend) Subscribe(ctx context.Context, id string, o assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	if b.sub.Add(1) == 1 {
		if err := fn(replayComplete); err != nil {
			return err
		}
		return assistant.ErrStreamBroken
	}
	return b.ownershipBackend.Subscribe(ctx, id, o, fn)
}

func (b *outageBackend) Send(ctx context.Context, m any, o assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	if _, ok := m.(string); !ok {
		<-ctx.Done()
		return o.ConversationID, ctx.Err()
	}
	// The foreign turn was already running before this send, during our outage.
	for _, frame := range []assistant.Message{userMsg("mine-u", "mine"), clientCallMsg("foreign-live", "foreign", "{}"), statusMsg("ended"), statusMsg("started"), textMsg("my-answer", "done"), statusMsg("ended")} {
		b.frames <- frame
		if frame.Content.Type == assistant.ContentClientToolCall {
			var r assistant.AssistantResponse
			r.Data.Attributes.StructuredMessage = frame
			if err := fn(r); err != nil {
				return o.ConversationID, err
			}
		}
	}
	<-ctx.Done()
	return o.ConversationID, ctx.Err()
}

func TestSendWaitsForReplayAfterOutage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &outageBackend{ownershipBackend: *newOwnershipBackend()}
		e := New(b, Config{ConversationID: testConversationID, PermissionsMode: ModeSkipPermissions})
		defer e.Close()
		e.do(e.bind)
		synctest.Wait()
		if s := e.State(); s.Connection.State != ConnectionReconnecting || s.Turn != TurnIdle {
			t.Fatalf("setup=%+v", s)
		}
		// Another session starts while we are offline; it is not concurrent with our send.
		b.mu.Lock()
		b.log = []assistant.Message{userMsg("remote-u", "started while offline"), statusMsg("started")}
		b.mu.Unlock()
		var foreign atomic.Int32
		tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "foreign"}, Handler: func(context.Context, ToolCall) (ToolResult, error) {
			foreign.Add(1)
			return ToolResult{Output: "ran"}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Until a replay shows the conversation, the prompt waits.
		if _, err = e.StartTurn(ctx, TurnInput{Message: "mine", Tools: tools}); !errors.Is(err, ErrSendBlocked) {
			t.Fatalf("StartTurn during an outage = %v, want ErrSendBlocked", err)
		}
		if q := e.State().Queued; len(q) != 1 || q[0].Message != "mine" {
			t.Fatalf("queued = %+v", q)
		}
		synctest.Wait()
		if foreign.Load() != 0 {
			t.Fatalf("executed %d foreign calls from a turn already running during the outage", foreign.Load())
		}
	})
}

// lostBackend fails each send before any response, as when the connection
// resets mid-request.
type lostBackend struct {
	scriptBackend
	sends atomic.Int32
}

func (b *lostBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	b.sends.Add(1)
	return "", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
}

// A send that fails before any response ends the round with the error and
// puts the prompt back in the queue, never resent on its own. Once the
// conversation shows the prompt arrived after all, the error clears and the
// prompt leaves the queue, as in the web client.
func TestFailedSendClearsOnceTheConversationShowsIt(t *testing.T) {
	b := &lostBackend{}
	e := New(b, Config{ConversationID: testConversationID})
	defer e.Close()
	awaitCurrent(t, e)
	evs := drain(t, e, TurnInput{Message: "hello"})
	if last := evs[len(evs)-1]; last.Kind != EventError {
		t.Fatalf("last = %+v, want an undelivered error", last)
	}
	if s := e.State(); !s.Unconfirmed || len(s.Queued) != 1 {
		t.Fatalf("state = %+v, want the failed prompt queued", s)
	}
	// The request reached the conversation after all.
	if _, err := b.conversations.Send(t.Context(), "hello", assistant.SendOptions{ConversationID: testConversationID}, func(assistant.AssistantResponse) error { return nil },
		func(func(assistant.AssistantResponse) error) (string, error) { return testConversationID, nil }); err != nil {
		t.Fatal(err)
	}
	awaitState(t, e, func(s State) bool { return !s.Unconfirmed })
	s := e.State()
	prompts := 0
	for _, b := range s.Blocks {
		if b.Role == assistant.RoleUser {
			prompts++
		}
	}
	if len(s.Queued) != 0 || prompts != 1 || b.sends.Load() != 1 {
		t.Fatalf("queued = %v, prompts shown = %d, sends = %d; want the prompt shown once and sent once", s.Queued, prompts, b.sends.Load())
	}
}

// shownThenLostBackend delivers each message to the conversation, which
// shows it, then fails the send before this client sees any response.
type shownThenLostBackend struct{ scriptBackend }

func (b *shownThenLostBackend) Send(ctx context.Context, message any, opts assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	return b.conversations.Send(ctx, message, opts, func(assistant.AssistantResponse) error { return nil }, func(emit func(assistant.AssistantResponse) error) (string, error) {
		var ar assistant.AssistantResponse
		ar.Data.Attributes.StructuredMessage = textMsg("answer", "delivered answer")
		if err := emit(ar); err != nil {
			return opts.ConversationID, err
		}
		return opts.ConversationID, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
	})
}

// A send that fails once the conversation already shows its prompt leaves
// no error to clear and nothing to send again.
func TestFailedSendAlreadyShownIsConfirmed(t *testing.T) {
	e := New(&shownThenLostBackend{}, Config{ConversationID: testConversationID})
	defer e.Close()
	awaitCurrent(t, e)
	drain(t, e, TurnInput{Message: "hello"})
	if s := e.State(); s.Unconfirmed || len(s.Queued) != 0 {
		t.Fatalf("unconfirmed = %t, queued = %v; want the shown prompt confirmed", s.Unconfirmed, s.Queued)
	}
}

// retryBackend fails every send, and every subscription while offline; the
// first subscription is followed until drop closes.
type retryBackend struct {
	offline       atomic.Bool
	subscriptions atomic.Int32
	drop          chan struct{}
}

func (b *retryBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	return "", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("offline")}
}

func (b *retryBackend) Subscribe(ctx context.Context, _ string, _ assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	n := b.subscriptions.Add(1)
	if b.offline.Load() {
		return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}
	}
	if err := fn(replayComplete); err != nil {
		return err
	}
	if n == 1 {
		select {
		case <-b.drop:
			return errors.New("dropped")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

// Asking to send a failed prompt again tries the conversation at once, even
// deep into a long backoff.
func TestRetryReconnectsAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &retryBackend{drop: make(chan struct{})}
		e := New(b, Config{ConversationID: testConversationID})
		defer e.Close()
		e.do(e.bind)
		synctest.Wait()
		b.offline.Store(true)
		drain(t, e, TurnInput{Message: "retry me"})
		close(b.drop)
		synctest.Wait()
		for n := e.State().Connection.Failures; n < 6; n = e.State().Connection.Failures {
			if n < 1 {
				t.Fatal("no outage")
			}
			time.Sleep(min(time.Second<<min(n-1, 16), 30*time.Second))
			synctest.Wait()
		}
		b.offline.Store(false)
		before := b.subscriptions.Load()
		e.Retry()
		synctest.Wait()
		if b.subscriptions.Load() == before || e.State().Connection.State != ConnectionConnected {
			t.Fatalf("after Retry: %d attempts, connection %v; want a new attempt at once", b.subscriptions.Load()-before, e.State().Connection.State)
		}
	})
}

// Restoring a conversation whose turn still runs leaves its server tool
// running; only a turn that is over interrupts what it left unfinished.
func TestSettleLeavesARunningTurnsToolsRunning(t *testing.T) {
	for _, test := range []struct {
		name        string
		delay       time.Duration
		messages    []assistant.Message
		interrupted bool
	}{
		{"turn running", 0, []assistant.Message{userMsg("u1", "one"), statusMsg("started"), serverCallMsg("s1", "search_logs", "{}"), replayComplete}, false},
		{"turn running, replay after the restore", 300 * time.Millisecond, []assistant.Message{userMsg("u1", "one"), statusMsg("started"), serverCallMsg("s1", "search_logs", "{}"), replayComplete}, false},
		{"turn over", 0, []assistant.Message{userMsg("u1", "one"), serverCallMsg("s1", "search_logs", "{}"), replayComplete}, true},
		{"turn over, replay after the restore", 300 * time.Millisecond, []assistant.Message{userMsg("u1", "one"), serverCallMsg("s1", "search_logs", "{}"), replayComplete}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := following(&liveBackend{attempts: []liveAttempt{{delay: test.delay, messages: test.messages}}})
				defer e.Close()
				if test.delay == 0 {
					synctest.Wait()
				}
				// The restore settles its tools, then the replay arrives.
				e.SettleRestoredTools(nil)
				time.Sleep(time.Second)
				synctest.Wait()
				i := slices.IndexFunc(e.State().Blocks, func(b Block) bool { return b.Tool != nil })
				if i < 0 {
					t.Fatal("no tool block")
				}
				if got := e.State().Blocks[i].Tool.Status == ToolCancelled; got != test.interrupted {
					t.Fatalf("interrupted = %t, want %t (status %v)", got, test.interrupted, e.State().Blocks[i].Tool.Status)
				}
			})
		})
	}
}

// A new conversation drops a restore's settle still waiting on its replay:
// it belongs to the conversation left behind.
func TestNewConversationDropsAWaitingSettle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := following(&liveBackend{attempts: []liveAttempt{{delay: time.Hour, messages: []assistant.Message{replayComplete}}}})
		defer e.Close()
		e.SettleRestoredTools(nil)
		if get(e, func() bool { return e.unsettled == nil }) {
			t.Fatal("the settle did not wait on the replay")
		}
		if err := e.NewConversation(); err != nil {
			t.Fatal(err)
		}
		if !get(e, func() bool { return e.unsettled == nil }) {
			t.Fatal("the new conversation kept the old one's settle")
		}
	})
}

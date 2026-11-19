package tui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

func TestFakeTurnAndCommandsRenderLocalMessagesInOrder(t *testing.T) {
	backend := fake.New()
	// A conversation that exists is followed from the first turn, so its
	// content shows before the turn's failure does.
	id, err := backend.PushConversation(context.Background(), "", `say("ready")`)
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(backend, agent.Config{ConversationID: id}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.Update(tea.PasteMsg{Content: `load("notices.star", "rate_limit"); rate_limit()`})
	_, _ = m.submit()
	pumpUntilFree(t, m)
	if latestNotice(m).Level != chat.NoticeWarn {
		t.Fatalf("fake rate-limit severity = %v, want warning", latestNotice(m).Level)
	}
	_, _ = m.dispatchCommand("bogus", "")
	_, _ = m.dispatchCommand("copy", "")
	doc := ansi.Strip(m.list.Document())
	parts := []string{"Partial answer", "Rate limited", "Unknown command: /bogus", "Copied to clipboard."}
	previous := -1
	for _, part := range parts {
		at := strings.Index(doc, part)
		if at <= previous {
			t.Fatalf("%q out of order in transcript:\n%s", part, doc)
		}
		previous = at
	}
	// The conversation marks where the turn failed, apart from the answer:
	// /copy and /export take the answer alone.
	if len(m.state.Blocks) != 5 || m.state.Blocks[3].Error || !m.state.Blocks[4].Error || len(m.notices) != 3 {
		t.Fatalf("agent blocks=%+v local messages=%d", m.state.Blocks, len(m.notices))
	}
	failure := m.state.Blocks[4].Markdown.Content
	if text, ok := latestCopyableAssistantResponse(m.state.Blocks); !ok || !strings.HasPrefix(text, "Partial answer") || strings.Contains(text, failure) {
		t.Fatalf("/copy = %q, want the partial answer only", text)
	}
	if text, _ := conversationMarkdown(m.state.Blocks, "", ""); strings.Contains(text, failure) {
		t.Fatalf("/export holds the failure:\n%s", text)
	}
	_, _ = m.dispatchCommand("new", "")
	if len(m.state.Blocks) != 0 || len(m.notices) != 1 || latestNotice(m).Text != "Started a new conversation." {
		t.Fatalf("new conversation retained old messages: blocks=%d notices=%+v", len(m.state.Blocks), m.notices)
	}
}

func TestDelayedBrowserResultStaysOutOfNewConversation(t *testing.T) {
	m := New(agent.New(fake.New(), agent.Config{}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	oldEpoch := m.conversationEpoch
	m.startNewConversation()
	_, _ = m.Update(browserOpenResultMsg{epoch: oldEpoch, url: "https://example.com/old"})
	if len(m.notices) != 1 || latestNotice(m).Text != "Started a new conversation." {
		t.Fatalf("stale browser result entered new transcript: %+v", m.notices)
	}
}

func TestCommandNoticeVisibleWhileScrolledUp(t *testing.T) {
	for _, tc := range []struct{ command, response string }{
		{"bogus", "Unknown command: /bogus"},
		{"copy", "No completed assistant response is available to copy."},
	} {
		t.Run(tc.command, func(t *testing.T) {
			m := New(agent.New(fake.New(), agent.Config{}))
			_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			for range 20 {
				m.postNotice(notice(chat.NoticeInfo, nil, "Earlier message"))
			}
			m.list.ScrollToTop()
			if m.list.Following() {
				t.Fatal("setup: transcript is still following the tail")
			}

			_, _ = m.dispatchCommand(tc.command, "")
			if !m.list.Following() || !strings.Contains(ansi.Strip(m.list.Render()), tc.response) {
				t.Fatalf("command response is outside the viewport:\n%s", ansi.Strip(m.list.Render()))
			}

			m.list.ScrollToTop()
			m.postNotice(notice(chat.NoticeError, nil, "Assistant service failed."))
			if m.list.Following() || strings.Contains(ansi.Strip(m.list.Render()), "Assistant service failed.") {
				t.Fatal("asynchronous notice changed the reader's scroll position")
			}
		})
	}
}

func TestPendingCommandsUsePlaceholderWithoutTranscriptNotice(t *testing.T) {
	m := New(agent.New(fake.New(), agent.Config{}))
	m.op.kind = opTurn
	_, _ = m.dispatchCommand("new", "")
	_, _ = m.dispatchCommand("new", "")
	if got := m.promptPlaceholder(); got != "Starting a new conversation…" || len(m.notices) != 0 {
		t.Fatalf("pending /new: placeholder=%q notices=%+v", got, m.notices)
	}
	_, _ = m.dispatchCommand("logout", "")
	if got := m.promptPlaceholder(); got != "Logging out…" || len(m.notices) != 0 {
		t.Fatalf("pending /logout: placeholder=%q notices=%+v", got, m.notices)
	}
}

// pumpUntilFree applies engine events until the operation ended and the
// conversation is free: a turn's run ends with its response, and the
// conversation shows the rest of the turn after.
func pumpUntilFree(t *testing.T, m *Model) {
	t.Helper()
	for m.op.busy() || m.sendBlocked() || m.state.ConversationID != "" && m.state.Connection.State != agent.ConnectionConnected {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
}

func latestNotice(m *Model) chat.Notice {
	if len(m.notices) == 0 {
		return chat.Notice{}
	}
	return m.notices[len(m.notices)-1].Notice
}

func TestDropMidAnswerShowsEachPartOnce(t *testing.T) {
	m := New(agent.New(fake.New(), agent.Config{}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.Update(tea.PasteMsg{Content: `load("connection.star", "drop_mid_answer"); drop_mid_answer()`})
	_, _ = m.submit()
	pumpUntilFree(t, m)
	doc := ansi.Strip(m.list.Document())
	for _, part := range []string{"first half", "word-19", "second half", "more-19"} {
		if strings.Count(doc, part) != 1 {
			t.Fatalf("%q appears %d times:\n%s", part, strings.Count(doc, part), doc)
		}
	}
}

// Each outage that showed the reconnecting row posts one notice once
// connected again; one too brief to show the row stays silent.
func TestOutageNotice(t *testing.T) {
	start := time.Now()
	for _, tc := range []struct {
		name          string
		lost          time.Duration
		before, after agent.TurnState
		want          int
	}{
		{"brief drop", 3 * time.Second, agent.TurnIdle, agent.TurnIdle, 0},
		{"brief drop mid-turn", 3 * time.Second, agent.TurnRunning, agent.TurnRunning, 0},
		{"brief drop as the turn finished", 3 * time.Second, agent.TurnRunning, agent.TurnIdle, 0},
		{"long outage", 42 * time.Second, agent.TurnIdle, agent.TurnIdle, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(agent.New(fake.New(), agent.Config{}))
			_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m.state.Turn = tc.before
			m.state.Connection = agent.Connection{State: agent.ConnectionReconnecting, Since: start}
			m.observeConnection()
			m.state.Connection = agent.Connection{State: agent.ConnectionDisconnected, Since: start.Add(time.Second)}
			m.observeConnection()
			m.state.Turn = tc.after
			m.state.Connection = agent.Connection{State: agent.ConnectionConnected, Since: start.Add(tc.lost)}
			m.observeConnection()
			m.observeConnection()
			if got := strings.Count(ansi.Strip(m.list.Document()), "Connection restored."); got != tc.want {
				t.Fatalf("outage notices = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestConnectionStatusRow(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name string
		conn agent.Connection
		turn agent.TurnState
		want string
		// remote is whether a prompt waits for the turn rather than sending.
		remote bool
	}{
		{"connected", agent.Connection{State: agent.ConnectionConnected}, agent.TurnIdle, "", false},
		{"brief reconnect", agent.Connection{State: agent.ConnectionReconnecting, Since: now.Add(-time.Second)}, agent.TurnIdle, "", false},
		{"reconnect just under 10s", agent.Connection{State: agent.ConnectionReconnecting, Since: now.Add(-reconnectingDelay + time.Millisecond)}, agent.TurnIdle, "", false},
		{"reconnect for 10s", agent.Connection{State: agent.ConnectionReconnecting, Since: now.Add(-reconnectingDelay)}, agent.TurnIdle, "Reconnecting.", false},
		{"long reconnect mid-turn", agent.Connection{State: agent.ConnectionReconnecting, Since: now.Add(-time.Hour)}, agent.TurnRunning, "Reconnecting.", true},
		{"refused", agent.Connection{State: agent.ConnectionDisconnected, Err: fmt.Errorf("token: %w", auth.ErrReauthRequired)}, agent.TurnRunning, "login expired", false},
		{"remote turn", agent.Connection{State: agent.ConnectionConnected}, agent.TurnRunning, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(agent.New(fake.New(), agent.Config{}))
			_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m.state.Connection, m.state.Turn = tc.conn, tc.turn
			if got := m.remoteTurn(); got != tc.remote {
				t.Fatalf("remoteTurn = %v, want %v", got, tc.remote)
			}
			got := m.connectionStatus(now).Text
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
			// The row stays below every notice, even one posted after it.
			m.syncTranscript()
			m.postNotice(notice(chat.NoticeInfo, nil, "Later notice"))
			doc := ansi.Strip(m.list.Document())
			if tc.want != "" && strings.Index(doc, tc.want) < strings.Index(doc, "Later notice") {
				t.Fatalf("status row is not last:\n%s", doc)
			}
		})
	}
}

// failingSubscriptions fails every conversation subscription, as when the
// network is down, and counts them.
type failingSubscriptions struct {
	*fake.Fake
	attempts atomic.Int32
}

func (b *failingSubscriptions) Subscribe(ctx context.Context, id string, opts assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	if opts.TaskID != "" {
		return b.Fake.Subscribe(ctx, id, opts, fn)
	}
	b.attempts.Add(1)
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}
}

// Coming back to the terminal tries the conversation again without waiting.
func TestFocusTriesTheConnectionAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &failingSubscriptions{Fake: fake.New()}
		id, err := backend.PushConversation(t.Context(), "Earlier", `say("earlier")`)
		if err != nil {
			t.Fatal(err)
		}
		engine := agent.New(backend, agent.Config{ConversationID: id})
		defer engine.Close()
		// Following starts once the conversation is restored.
		events, unsubscribe := engine.Subscribe()
		defer unsubscribe()
		run, err := engine.Restore(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for range agent.Follow(events, run) {
		}
		m := New(engine)
		defer m.unsubscribe()
		synctest.Wait()
		before := backend.attempts.Load()
		_, _ = m.Update(tea.FocusMsg{})
		synctest.Wait()
		if got := backend.attempts.Load(); got != before+1 {
			t.Fatalf("attempts = %d after focus, want %d", got, before+1)
		}
	})
}

// offlineOnceBackend fails its first send before any response and its first
// subscription, as when the network is down when the first prompt goes out.
type offlineOnceBackend struct {
	*fake.Fake
	sends, subscriptions atomic.Int32
}

func (b *offlineOnceBackend) Send(ctx context.Context, message any, opts assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	if b.sends.Add(1) == 1 {
		return opts.ConversationID, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}
	}
	return b.Fake.Send(ctx, message, opts, fn)
}

func (b *offlineOnceBackend) Subscribe(ctx context.Context, id string, opts assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	if opts.TaskID == "" && b.subscriptions.Add(1) == 1 {
		return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("offline")}
	}
	return b.Fake.Subscribe(ctx, id, opts, fn)
}

// A prompt whose send failed waits until the user sends it again, even once
// the connection is back: the conversation never showed it, and it is never
// resent on its own.
func TestFailedSendWaitsForRetry(t *testing.T) {
	f := fake.New()
	id, err := f.PushConversation(context.Background(), "", `say("ready")`)
	if err != nil {
		t.Fatal(err)
	}
	b := &offlineOnceBackend{Fake: f}
	m := New(agent.New(b, agent.Config{ConversationID: id}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(m, `say("hello")`)
	_, _ = m.submit()
	pumpUntilFree(t, m)
	if n := b.sends.Load(); n != 1 || len(m.state.Queued) != 1 || !m.state.Unconfirmed {
		t.Fatalf("sends = %d, queued = %d, unconfirmed = %t; want the failed prompt held", n, len(m.state.Queued), m.state.Unconfirmed)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // an empty draft sends it again
	pumpUntilFree(t, m)
	if n := b.sends.Load(); n != 2 || len(m.state.Queued) != 0 || m.state.Unconfirmed {
		t.Fatalf("after retry: sends = %d, queued = %d, unconfirmed = %t", n, len(m.state.Queued), m.state.Unconfirmed)
	}
}

// shownThenLostBackend shows each message in the conversation, then fails
// the send before this client sees any response.
type shownThenLostBackend struct{ cancellableConversationBackend }

func (b *shownThenLostBackend) Send(ctx context.Context, message any, opts assistant.SendOptions, fn func(assistant.AssistantResponse) error) (string, error) {
	return b.conversations.Send(ctx, message, opts, func(assistant.AssistantResponse) error { return nil }, func(emit func(assistant.AssistantResponse) error) (string, error) {
		var ar assistant.AssistantResponse
		ar.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("delivered answer"))
		if err := emit(ar); err != nil {
			return opts.ConversationID, err
		}
		return opts.ConversationID, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
	})
}

// A send that fails once the conversation already shows its message leaves
// no failure notice: the message arrived.
func TestFailedSendAlreadyShownHasNoNotice(t *testing.T) {
	e := agent.New(&shownThenLostBackend{}, agent.Config{ConversationID: "00000000-0000-4000-8000-000000000001"})
	defer e.Close()
	m := New(e)
	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	setConversationInput(m, "confirmed prompt")
	_, _ = m.submit()
	drainConversationRemote(t, m)
	if len(m.notices) != 0 {
		t.Fatalf("notices = %+v, want none for a message the conversation shows", m.notices)
	}
}

// In a new conversation, Enter retries the failed first prompt rather than
// opening a recent conversation the welcome screen offers.
func TestEnterRetriesAFailedFirstPrompt(t *testing.T) {
	b := &offlineOnceBackend{Fake: fake.New()}
	m := New(agent.New(b, agent.Config{}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.resume = *resumeFixture(4)
	if !m.showResume() {
		t.Fatal("the welcome screen offers no recent conversation")
	}
	m.editor.Update(tea.PasteMsg{Content: `say("hello")`})
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	for m.op.busy() {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	if b.sends.Load() != 1 || len(m.state.Queued) != 1 {
		t.Fatalf("sends = %d, queued = %d; want the failed prompt held", b.sends.Load(), len(m.state.Queued))
	}
	id := m.state.ConversationID
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Sent at once, or once the conversation is back within reach.
	for b.sends.Load() < 2 || m.op.busy() {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	if b.sends.Load() != 2 || len(m.state.Queued) != 0 || m.state.ConversationID != id {
		t.Fatalf("sends = %d, queued = %d, conversation %q (was %q); want the prompt sent again here", b.sends.Load(), len(m.state.Queued), m.state.ConversationID, id)
	}
}

// A turn another session runs shows as one running here: the composer's
// sweep moves and prompts queued meanwhile move as they do behind a local
// turn, with no row of their own.
func TestRemoteTurnLooksLikeALocalOne(t *testing.T) {
	m := New(agent.New(fake.New(), agent.Config{}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.styles.Input.SweepMotion = true
	m.state.Connection = agent.Connection{State: agent.ConnectionConnected}
	m.state.Queued = []agent.QueuedPrompt{{ID: 1, Message: "next"}}
	for _, turn := range []agent.TurnState{agent.TurnIdle, agent.TurnRunning} {
		m.state.Turn = turn
		m.syncQueue()
		running := turn == agent.TurnRunning
		if m.borderSweepWanted() != running || m.editor.QueuePaused() == running || !m.connectionStatus(time.Now()).Empty() {
			t.Fatalf("turn %v: sweep = %t, queue paused = %t, row = %q", turn, m.borderSweepWanted(), m.editor.QueuePaused(), m.connectionStatus(time.Now()).Text)
		}
	}
}

// The reconnecting row's dots follow the shared activity frame, and stand
// still as "Reconnecting…" with motion off.
func TestReconnectingRowDots(t *testing.T) {
	m := New(agent.New(fake.New(), agent.Config{}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	now := time.Now()
	m.state.Connection = agent.Connection{State: agent.ConnectionReconnecting, Since: now.Add(-reconnectingDelay)}
	for _, tc := range []struct {
		frame int
		want  string
	}{{0, "Reconnecting."}, {8, "Reconnecting.."}, {16, "Reconnecting..."}, {24, "Reconnecting."}} {
		m.animActivity.frame = tc.frame
		if got := m.connectionStatus(now).Text; got != tc.want {
			t.Fatalf("frame %d: row = %q, want %q", tc.frame, got, tc.want)
		}
	}
	if !m.reconnectingAnimationWanted(now) {
		t.Fatal("the row's dots do not move")
	}
	m.styles.Chat.Motion = false
	if got := m.connectionStatus(now).Text; got != "Reconnecting…" || m.reconnectingAnimationWanted(now) {
		t.Fatalf("motion off: row = %q, animated = %t; want a still row", got, m.reconnectingAnimationWanted(now))
	}
}

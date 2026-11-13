package tui

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
)

// gateBackend holds its first request until open is called, then fails it
// with err, if set. Later requests return at once.
type gateBackend struct {
	release chan struct{}
	open    func()
	err     error

	mu   sync.Mutex
	sent int
}

func (b *gateBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.mu.Lock()
	b.sent++
	first := b.sent == 1
	b.mu.Unlock()
	if !first {
		return "c", nil
	}
	select {
	case <-b.release:
		return "c", b.err
	case <-ctx.Done():
		return "c", ctx.Err()
	}
}

// newModelInTurn returns a model whose turn for "first" holds in the backend.
func newModelInTurn(t *testing.T, err error) (*Model, *gateBackend) {
	t.Helper()
	backend := &gateBackend{release: make(chan struct{}), err: err}
	backend.open = sync.OnceFunc(func() { close(backend.release) })
	engine := agent.New(backend, agent.Config{})
	t.Cleanup(func() { backend.open(); engine.Close() })
	m := New(engine)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(m, "first")
	m.submit()
	if m.op.run == 0 {
		t.Fatal("first prompt did not start a turn")
	}
	return m, backend
}

func userTexts(m *Model) []string {
	var texts []string
	for _, p := range m.state.UserPrompts() {
		texts = append(texts, p.Text)
	}
	return texts
}

func TestPromptsQueueBehindTheRunningTurn(t *testing.T) {
	m, backend := newModelInTurn(t, nil)
	run := m.op.run
	setConversationInput(m, "second")
	m.editor.AddAttachment(editor.Attachment{Type: "service", ID: "api", Label: "API"})
	m.submit()
	setConversationInput(m, "third")
	m.submit()
	setConversationInput(m, "unfinished draft")
	m.relayout()
	if m.op.run != run || m.editor.QueueLen() != 2 || m.editor.QueuePaused() {
		t.Fatalf("run %d (want %d), queued %d, paused %v", m.op.run, run, m.editor.QueueLen(), m.editor.QueuePaused())
	}

	backend.open()
	drainConversationRemote(t, m)
	if got := userTexts(m); !reflect.DeepEqual(got, []string{"first", "second\n\nthird"}) {
		t.Fatalf("prompts = %q", got)
	}
	if prompts := m.state.UserPrompts(); len(prompts[1].Context) != 1 || prompts[1].Context[0].ID != "api" {
		t.Fatalf("queued context = %+v", prompts[1].Context)
	}
	if m.editor.Value() != "unfinished draft" || m.editor.QueueLen() != 0 {
		t.Fatalf("draft %q, queued %d", m.editor.Value(), m.editor.QueueLen())
	}
}

// A turn that is cancelled or fails leaves its queue paused: new prompts join
// it, and Enter on an empty draft sends it.
func TestInterruptedTurnPausesTheQueueUntilEnter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		cancel bool
	}{
		{"cancelled", nil, true},
		{"failed", errors.New("backend failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, backend := newModelInTurn(t, tc.err)
			setConversationInput(m, "later")
			m.submit()
			if tc.cancel {
				m.cancelOperation()
			}
			backend.open()
			drainConversationRemote(t, m)
			m.relayout()
			if !m.editor.QueuePaused() || m.editor.QueueLen() != 1 {
				t.Fatalf("paused %v, queued %d", m.editor.QueuePaused(), m.editor.QueueLen())
			}

			setConversationInput(m, "more")
			m.submit()
			m.relayout()
			if m.op.busy() || m.editor.QueueLen() != 2 || !m.editor.QueuePaused() {
				t.Fatal("a prompt submitted to a paused queue did not join it")
			}
			setConversationInput(m, "")
			m.submit()
			drainConversationRemote(t, m)
			m.relayout()
			if got := userTexts(m); !reflect.DeepEqual(got, []string{"first", "later\n\nmore"}) || m.editor.QueueLen() != 0 {
				t.Fatalf("prompts = %q, queued %d", got, m.editor.QueueLen())
			}
		})
	}
}

func TestQueueControlsPrecedeSubmissionAndCancellation(t *testing.T) {
	m, _ := newModelInTurn(t, nil)
	setConversationInput(m, "later")
	m.submit()
	m.editor.SetValue("draft")
	m.editor.AddAttachment(editor.Attachment{Type: "service", ID: "api"})
	m.relayout()
	key := func(code rune, mod tea.KeyMod) { m.Update(tea.KeyPressMsg{Code: code, Mod: mod}) }
	key(tea.KeyTab, 0)   // attachments
	key(tea.KeyEnter, 0) // return to text, not submit
	if m.editor.QueueLen() != 1 || m.editor.Value() != "draft" {
		t.Fatal("Enter on attachments submitted draft")
	}
	key(tea.KeyTab, tea.ModShift) // queue
	key(tea.KeyEscape, 0)
	if m.op.stop != stopNone {
		t.Fatal("Escape on queue cancelled turn")
	}
	// A click on the close control removes the queued prompt without touching draft.
	rect := m.frame.queue
	m.Update(tea.MouseClickMsg{X: rect.Max.X - 2, Y: rect.Min.Y + 1, Button: tea.MouseLeft})
	if m.editor.QueueLen() != 0 || len(m.state.Queued) != 0 || m.editor.Value() != "draft" || len(m.editor.Attachments()) != 1 {
		t.Fatal("queue click changed draft or kept the prompt queued")
	}
	key(tea.KeyEscape, 0)
	if m.op.stop != stopAll {
		t.Fatal("Escape in text did not interrupt")
	}
}

// Prompts submitted after the engine ended the shown turn, before its end
// is read, wait in the queue: the end is shown first, error included, then a
// completed turn's successor sends them all, and a failed one pauses them.
func TestPromptsSubmittedBeforeAnUnreadTurnEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"completed", nil},
		{"failed", errors.New("backend failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, backend := newModelInTurn(t, tc.err)
			events, unsubscribe := m.engine.Subscribe()
			defer unsubscribe()
			first := m.op.run
			backend.open()
			for range agent.Follow(events, first) {
			}
			for _, prompt := range []string{"second", "third"} {
				setConversationInput(m, prompt)
				m.submit()
				if m.op.run != first || m.engine.OperationActive() {
					t.Fatalf("%q started a turn before the first one's end was read", prompt)
				}
			}
			drainConversationRemote(t, m)
			m.relayout()
			want, paused := []string{"first", "second\n\nthird"}, false
			if tc.err != nil {
				want, paused = []string{"first"}, true
				if !errors.Is(latestNotice(m).Err, tc.err) {
					t.Fatalf("the first turn's error was not shown: %+v", latestNotice(m))
				}
			}
			if got := userTexts(m); !reflect.DeepEqual(got, want) || m.editor.QueuePaused() != paused {
				t.Fatalf("prompts = %q (want %q), paused %v", got, want, m.editor.QueuePaused())
			}
		})
	}
}

func TestRenameKeepsPausedPromptsAndTurnAdmission(t *testing.T) {
	backend := &tuiRenameBackend{resumeBackend: newResumeBackend()}
	m := newRenameModel(t, backend, forkSourceID)
	if err := m.engine.Queue(agent.TurnInput{Message: "pending follow-up"}); err != nil {
		t.Fatal(err)
	}
	m.state = m.engine.State()
	m.relayout()
	if !m.editor.QueuePaused() {
		t.Fatal("idle queue was not paused")
	}
	rename := m.startRename("New title")
	m.relayout()
	if !m.editor.QueuePaused() {
		t.Fatal("rename made the queue appear to be draining")
	}
	generation := m.op.gen
	setConversationInput(m, "keep this draft")
	m.submit()
	// The shared admission gate must reject the turn before SendQueued can
	// remove a prompt or start work in the engine.
	m.startTurn(m.engine.SendQueued, agent.TurnInput{})
	if m.op.kind != opRename || m.op.gen != generation || m.editor.Value() != "keep this draft" {
		t.Fatal("queued prompts bypassed rename admission or replaced its draft")
	}
	if m.engine.OperationActive() || len(m.engine.State().Queued) != 1 {
		t.Fatal("turn admission changed the engine queue during rename")
	}
	applyRename(t, m, rename)
	if m.op.busy() || !m.editor.QueuePaused() || m.editor.QueueLen() != 1 || len(m.state.UserPrompts()) != 0 {
		t.Fatal("rename completion sent or dropped the paused queue")
	}
}

// The startup prompt is sent verbatim, after any history, and only when the
// restore it waits for ends unless interrupted; a stopped restore parks it.
func TestInitialPrompt(t *testing.T) {
	const prompt = `say("initial reply")`
	const earlier = `say("earlier reply")`
	logout := func(err error) LogoutFunc {
		return func(context.Context) (bool, error, error) { return true, nil, err }
	}
	cases := []struct {
		name       string
		prompt     string
		restore    bool
		logout     LogoutFunc
		interrupt  func(*Model)
		wantSent   []string
		wantQueued []string
	}{
		{name: "fresh chat sends a slash command as text", prompt: "/resume", wantSent: []string{"/resume"}},
		{name: "restore sends it after the history", prompt: prompt, restore: true, wantSent: []string{prompt}},
		{
			name: "stopped restore parks it", prompt: prompt, restore: true,
			interrupt: (*Model).cancelOperation, wantQueued: []string{prompt},
		},
		{
			name: "logout never sends it", prompt: prompt, restore: true, logout: logout(nil),
			interrupt: func(m *Model) { m.dispatchCommand("logout", "") }, wantQueued: []string{prompt},
		},
		{
			name: "failed logout parks it", prompt: prompt, restore: true, logout: logout(context.DeadlineExceeded),
			interrupt: func(m *Model) { m.dispatchCommand("logout", "") }, wantQueued: []string{prompt},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fake.New()
			config := agent.Config{}
			if tc.restore {
				id, err := f.PushConversation(t.Context(), "Earlier", earlier)
				if err != nil {
					t.Fatal(err)
				}
				config.ConversationID = id
			}
			m := New(agent.New(f, config), Config{InitialPrompt: tc.prompt, Logout: tc.logout})
			_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			_ = m.initChat()
			m.editor.SetValue("draft")
			if tc.interrupt != nil {
				tc.interrupt(m)
			}
			// Drain the runs; a queued logout starts with the last one's end.
			var last tea.Cmd
			for m.op.run != 0 {
				_, last = m.Update(runConversationCmd(t, waitEvent(m.events)))
			}
			if m.op.kind == opLogout {
				for _, msg := range flattenMsgs(t, last) {
					if result, ok := msg.(logoutResultMsg); ok {
						_, _ = m.Update(result)
					}
				}
			}
			if m.op.busy() {
				t.Fatalf("operation %v still running", m.op.kind)
			}
			var queued []string
			for _, q := range m.state.Queued {
				queued = append(queued, q.Message)
			}
			// The history may load before a stop lands, and always precedes the prompt.
			got := slices.DeleteFunc(userTexts(m), func(text string) bool { return text == earlier })
			if !slices.Equal(got, tc.wantSent) || !slices.Equal(queued, tc.wantQueued) {
				t.Fatalf("sent = %q, queued = %q; want %q, %q", got, queued, tc.wantSent, tc.wantQueued)
			}
			if m.picker != nil || m.editor.Value() != "draft" {
				t.Fatalf("picker open = %t, editor = %q; want no command run and the draft kept", m.picker != nil, m.editor.Value())
			}
		})
	}
}

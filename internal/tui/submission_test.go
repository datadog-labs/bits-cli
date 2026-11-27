package tui

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
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

// A failed turn leaves its queue paused until Enter. An empty draft sends it;
// a new prompt sends after it.
func TestFailedTurnPausesTheQueueUntilEnter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		draft string
		want  string
	}{
		{"empty draft", "", "later"},
		{"new prompt", "more", "later\n\nmore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, backend := newModelInTurn(t, errors.New("backend failed"))
			setConversationInput(m, "later")
			m.submit()
			backend.open()
			drainConversationRemote(t, m)
			m.relayout()
			if !m.editor.QueuePaused() || m.editor.QueueLen() != 1 {
				t.Fatalf("paused %v, queued %d", m.editor.QueuePaused(), m.editor.QueueLen())
			}

			setConversationInput(m, tc.draft)
			m.submit()
			drainConversationRemote(t, m)
			m.relayout()
			if got := userTexts(m); !reflect.DeepEqual(got, []string{"first", tc.want}) || m.editor.QueueLen() != 0 {
				t.Fatalf("prompts = %q, queued %d", got, m.editor.QueueLen())
			}
		})
	}
}

// Esc returns unsent prompts to the composer, in front of the draft as typed:
// its whitespace, its caret, and its attachments, which win over a queued
// copy of the same item.
func TestEscapeReturnsQueuedPromptsToTheDraft(t *testing.T) {
	const queued = "check staging\n\ncompare with yesterday"
	old := editor.Attachment{Type: "service", ID: "checkout", Label: "old"}
	checkout := editor.Attachment{Type: "service", ID: "checkout", Label: "Checkout", SearchFlowID: "draft-selection"}
	latency := editor.Attachment{Type: "dashboard", ID: "latency"}
	tail := strings.Repeat("wrapped text ", 20)
	for _, tc := range []struct {
		name        string
		draft       string
		attachments []editor.Attachment
		keys        []tea.KeyPressMsg // before Esc
		want        string            // after typing X
		chips       []editor.Attachment
	}{
		{name: "no draft", want: queued + "X", chips: []editor.Attachment{old}},
		{
			name:        "attachments",
			draft:       "look for errors",
			attachments: []editor.Attachment{checkout, latency},
			want:        queued + "\n\nlook for errorsX",
			chips:       []editor.Attachment{checkout, latency},
		},
		{name: "whitespace", draft: "\n    ", want: queued + "\n\n\n    X", chips: []editor.Attachment{old}},
		{
			name:  "caret across wrapped lines",
			draft: "ab\n" + tail,
			keys:  []tea.KeyPressMsg{{Code: tea.KeyHome, Mod: tea.ModCtrl}, {Code: tea.KeyRight}},
			want:  queued + "\n\naXb\n" + tail,
			chips: []editor.Attachment{old},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newModelInTurn(t, nil)
			enter := tea.KeyPressMsg{Code: tea.KeyEnter}
			setConversationInput(m, "check staging")
			m.editor.AddAttachment(old)
			m.Update(enter)
			setConversationInput(m, "compare with yesterday")
			m.Update(enter)
			setConversationInput(m, tc.draft)
			for _, attachment := range tc.attachments {
				m.editor.AddAttachment(attachment)
			}
			for _, key := range tc.keys {
				m.Update(key)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			drainConversationRemote(t, m)
			m.Update(tea.PasteMsg{Content: "X"})
			if got := m.editor.Value(); got != tc.want || len(m.state.Queued) != 0 || m.engine.OperationActive() {
				t.Fatalf("draft = %q, want %q, queue = %+v, busy = %v", got, tc.want, m.state.Queued, m.engine.OperationActive())
			}
			if got := m.editor.Attachments(); !reflect.DeepEqual(got, tc.chips) {
				t.Fatalf("chips = %+v, want %+v", got, tc.chips)
			}
		})
	}
}

// Prompts that one draft cannot carry stay queued and paused, and the draft
// untouched.
func TestEscapeKeepsPromptsADraftCannotCarry(t *testing.T) {
	long := strings.Repeat("line\n", 275) + "last line"
	image := string(assistant.EntityImageURL)
	override := []assistant.SkillOverride{{Name: "skills-library/clear-writing", Source: assistant.SkillSourceSkillsLibrary, Enabled: true}}
	for _, tc := range []struct {
		name        string
		queued      agent.TurnInput
		draft       string
		attachments []editor.Attachment
	}{
		{
			name:   "skill",
			queued: agent.TurnInput{Message: "/clear-writing", SkillOverrides: override},
		},
		{
			name:   "skill and a draft",
			queued: agent.TurnInput{Message: "/clear-writing", SkillOverrides: override},
			draft:  "draft",
		},
		{
			// Queued as text from " /new"; as a draft, Enter would run /new.
			name:   "command text",
			queued: agent.TurnInput{Message: "/new"},
			draft:  "draft",
		},
		{name: "command draft", queued: agent.TurnInput{Message: "queued"}, draft: "/help"},
		{
			name:   "editor row limit",
			queued: agent.TurnInput{Message: long},
			draft:  long,
		},
		{
			name: "image limit",
			queued: agent.TurnInput{Message: "queued", Context: contextFromAttachments([]editor.Attachment{
				{Type: image, ID: "one"}, {Type: image, ID: "two"}, {Type: image, ID: "three"},
			})},
			draft:       "draft",
			attachments: []editor.Attachment{{Type: image, ID: "four"}, {Type: image, ID: "five"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newModelInTurn(t, nil)
			if err := m.engine.Queue(tc.queued); err != nil {
				t.Fatal(err)
			}
			queued := m.engine.State().Queued
			setConversationInput(m, tc.draft)
			for _, attachment := range tc.attachments {
				m.editor.AddAttachment(attachment)
			}
			if m.editor.Value() != tc.draft {
				t.Fatal("draft did not fit before the merge")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			drainConversationRemote(t, m)
			if m.editor.Value() != tc.draft || !reflect.DeepEqual(m.editor.Attachments(), tc.attachments) {
				t.Fatal("keeping the queue changed the draft")
			}
			if !reflect.DeepEqual(m.state.Queued, queued) || !m.editor.QueuePaused() {
				t.Fatalf("queue = %+v, paused %v", m.state.Queued, m.editor.QueuePaused())
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
		name   string
		err    error
		escape bool
	}{
		{name: "completed"},
		{name: "failed", err: errors.New("backend failed")},
		{name: "escaped before its end is read", escape: true},
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
			if tc.escape {
				m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
				drainConversationRemote(t, m)
				if got := userTexts(m); !reflect.DeepEqual(got, []string{"first"}) || m.editor.Value() != "second\n\nthird" || len(m.state.Queued) != 0 {
					t.Fatalf("prompts = %q, draft = %q, queue = %+v", got, m.editor.Value(), m.state.Queued)
				}
				return
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

// sharedWithMeBackend serves one conversation another user owns.
type sharedWithMeBackend struct {
	t *testing.T
}

func (b *sharedWithMeBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	b.t.Fatal("sent a turn to a conversation the user does not own")
	return "", nil
}

func (*sharedWithMeBackend) ConversationHistory(context.Context, assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	r := history(assistant.AssistantMessage("m1", assistant.TextContent("Checkout latency is back to normal.")))
	r.Data.Attributes.OwnerUserUUID = "coworker-uuid"
	expires := shareExpiry.UnixMilli()
	r.Data.Attributes.SharedExpires = &expires
	return r, nil
}

func (*sharedWithMeBackend) CurrentUser(context.Context) (assistant.CurrentUser, error) {
	return assistant.CurrentUser{ID: "me-uuid"}, nil
}

func TestConversationSharedWithMeIsReadOnly(t *testing.T) {
	m := New(agent.New(&sharedWithMeBackend{t: t}, agent.Config{ConversationID: forkSourceID}))
	t.Cleanup(m.Detach)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	_ = m.initChat()
	drainConversationRemote(t, m)
	if !m.state.ReadOnly || !strings.Contains(latestNotice(m).Text, "/fork") {
		t.Fatalf("read-only = %v, notice = %q, want a hint to fork", m.state.ReadOnly, latestNotice(m).Text)
	}

	m.notices = nil
	setConversationInput(m, "Is it still fine?")
	m.submit()
	if m.editor.Value() != "Is it still fine?" || m.op.busy() {
		t.Fatalf("draft = %q, busy = %v; want the draft kept and no turn", m.editor.Value(), m.op.busy())
	}
	if !strings.Contains(latestNotice(m).Text, "/fork") {
		t.Fatalf("notice = %q, want a hint to fork", latestNotice(m).Text)
	}

	// Only the owner may change sharing; others may still see the link.
	for _, tt := range []struct{ argument, want string }{
		{"off", "Only the conversation's owner"},
		{"30d", "Only the conversation's owner"},
		{"", "Shared with your organization until Mar 2, 2030."},
	} {
		_, cmd := m.dispatchCommand("share", tt.argument)
		runShare(t, m, cmd)
		if m.commandPrompt != nil || m.op.busy() || !strings.Contains(latestNotice(m).Text, tt.want) {
			t.Fatalf("/share %s: picker = %v, busy = %v, notice = %q; want %q", tt.argument, m.commandPrompt != nil, m.op.busy(), latestNotice(m).Text, tt.want)
		}
	}

	setConversationInput(m, "/fork")
	m.submit()
	if m.op.kind != opFork {
		t.Fatalf("operation = %v, want /fork to run", m.op.kind)
	}
}

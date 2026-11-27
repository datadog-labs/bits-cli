package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// shareExpiry is when every share the test backend grants ends.
var shareExpiry = time.Date(2030, time.March, 2, 12, 0, 0, 0, time.Local)

type tuiShareBackend struct {
	spyBackend
	inputs []assistant.ShareConversationInput
	err    error
}

func (b *tuiShareBackend) ShareConversation(_ context.Context, in assistant.ShareConversationInput) (*assistant.ConversationSummary, error) {
	b.inputs = append(b.inputs, in)
	if b.err != nil {
		return nil, b.err
	}
	summary := &assistant.ConversationSummary{ConversationID: in.ConversationID}
	if in.Shared {
		ms := shareExpiry.UnixMilli()
		summary.SharedExpires = &ms
	}
	return summary, nil
}

// newShareModel opens conversationID, already shared when shared is set.
func newShareModel(t *testing.T, b *tuiShareBackend, conversationID string, shared bool) *Model {
	t.Helper()
	e := agent.New(b, agent.Config{ConversationID: conversationID})
	t.Cleanup(e.Close)
	if shared {
		if _, err := e.ShareConversation(context.Background(), assistant.ShareConversationInput{ConversationID: conversationID, Shared: true}); err != nil {
			t.Fatal(err)
		}
		b.inputs = nil
	}
	m := New(e)
	m.setMode(ModeChat)
	t.Cleanup(m.Detach)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

// runShare runs cmd, applies any share result it carries, and returns every
// message the two produced.
func runShare(t *testing.T, m *Model, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	for _, msg := range flattenMsgs(t, cmd) {
		out = append(out, msg)
		if result, ok := msg.(shareResultMsg); ok {
			_, next := m.Update(result)
			out = append(out, flattenMsgs(t, next)...)
		}
	}
	return out
}

func TestShare(t *testing.T) {
	const id = forkSourceID
	link := "https://app.datadoghq.com/bits/chat/" + id
	copied := "Shared with your organization until Mar 2, 2030. Share link: " + link
	thirty := 30
	tests := []struct {
		name       string
		shared     bool
		argument   string
		keys       []tea.KeyPressMsg // answers to the picker
		width      int               // the pane's width; 0 for 80 columns
		err        error
		noWebSite  bool
		wantInputs []assistant.ShareConversationInput
		wantNotice string // a substring of the last notice; "" for none
		wantCopy   bool
		wantStatus string
	}{
		{
			name: "picker shares a private conversation", keys: []tea.KeyPressMsg{{Code: tea.KeyEnter}},
			wantInputs: []assistant.ShareConversationInput{{ConversationID: id, Shared: true}},
			wantNotice: copied, wantCopy: true, wantStatus: "shared with your organization until Mar 2, 2030",
		},
		{
			name: "picker copies an active share without changing it", shared: true, keys: []tea.KeyPressMsg{{Code: tea.KeyEnter}},
			wantNotice: copied, wantCopy: true, wantStatus: "shared with your organization until Mar 2, 2030",
		},
		{
			name: "picker makes a shared conversation private", shared: true, keys: []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyEnter}},
			wantInputs: []assistant.ShareConversationInput{{ConversationID: id}},
			wantNotice: "This conversation is private.", wantStatus: "private",
		},
		{
			// The state may miss a share made on the web.
			name: "picking the current private choice still unshares", keys: []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyEnter}},
			wantInputs: []assistant.ShareConversationInput{{ConversationID: id}},
			wantNotice: "This conversation is private.", wantStatus: "private",
		},
		{
			name: "esc changes nothing", shared: true, keys: []tea.KeyPressMsg{{Code: tea.KeyEscape}},
			wantStatus: "shared with your organization until Mar 2, 2030",
		},
		{
			// Only the resize hint shows, so enter must not confirm.
			name: "a picker too wide for the pane can still be dismissed", width: 30, keys: []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: tea.KeyEscape}},
			wantStatus: "private",
		},
		{
			name: "off makes it private", shared: true, argument: "off",
			wantInputs: []assistant.ShareConversationInput{{ConversationID: id}},
			wantNotice: "This conversation is private.", wantStatus: "private",
		},
		{
			name: "days share for that long", argument: "30D",
			wantInputs: []assistant.ShareConversationInput{{ConversationID: id, Shared: true, TTLDays: &thirty}},
			wantNotice: copied, wantCopy: true, wantStatus: "shared with your organization until Mar 2, 2030",
		},
		{name: "days need a unit", argument: "30", wantNotice: shareUsage, wantStatus: "private"},
		{name: "days start at one", argument: "0d", wantNotice: shareUsage, wantStatus: "private"},
		{name: "days stop at the server's limit", argument: "1096d", wantNotice: shareUsage, wantStatus: "private"},
		{name: "unknown argument", argument: "soon", wantNotice: shareUsage, wantStatus: "private"},
		{
			name: "no web site", argument: "30d", noWebSite: true,
			wantInputs: []assistant.ShareConversationInput{{ConversationID: id, Shared: true, TTLDays: &thirty}},
			wantNotice: "Shared with your organization until Mar 2, 2030. Could not build its link.", wantStatus: "shared with your organization until Mar 2, 2030",
		},
		{
			name: "server error", shared: true, argument: "off", err: errors.New("offline"),
			wantInputs: []assistant.ShareConversationInput{{ConversationID: id}},
			wantNotice: "could not change sharing", wantStatus: "shared with your organization until Mar 2, 2030",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &tuiShareBackend{spyBackend: spyBackend{t: t, webOrigin: "https://app.datadoghq.com"}}
			if tt.noWebSite {
				b.webOrigin = ""
			}
			m := newShareModel(t, b, id, tt.shared)
			if tt.width > 0 {
				m.Update(tea.WindowSizeMsg{Width: tt.width, Height: 24})
			}
			b.err = tt.err
			_, cmd := m.dispatchCommand("share", tt.argument)
			msgs := runShare(t, m, cmd)
			m.relayout()
			if hint := "press esc"; tt.width > 0 && !strings.Contains(m.View().Content, hint) {
				t.Fatalf("the resize hint does not say %q:\n%s", hint, m.View().Content)
			}
			for _, key := range tt.keys {
				if m.commandPrompt == nil {
					t.Fatal("no picker is open")
				}
				_, cmd := m.Update(key)
				msgs = append(msgs, runShare(t, m, cmd)...)
			}
			if m.commandPrompt != nil {
				t.Fatal("the picker stayed open")
			}
			if !reflect.DeepEqual(b.inputs, tt.wantInputs) {
				t.Fatalf("share requests = %+v, want %+v", b.inputs, tt.wantInputs)
			}
			if n := lastNotice(m); tt.wantNotice == "" && n.Text != "" || !strings.Contains(n.Text, tt.wantNotice) {
				t.Fatalf("notice = %q, want %q", n.Text, tt.wantNotice)
			}
			copiedLink := false
			for _, msg := range msgs {
				copiedLink = copiedLink || msg == tea.SetClipboard(link)()
			}
			if copiedLink != tt.wantCopy {
				t.Fatalf("copied the link = %v, want %v", copiedLink, tt.wantCopy)
			}
			if len(b.inputs) > 0 && tt.err == nil {
				// Apply the state the share published.
				m.Update(waitEvent(m.events)())
			}
			if got := m.statusRuntime().Sharing; got != tt.wantStatus {
				t.Fatalf("status sharing = %q, want %q", got, tt.wantStatus)
			}
		})
	}
}

func TestShareNeedsASavedConversation(t *testing.T) {
	b := &tuiShareBackend{spyBackend: spyBackend{t: t}}
	m := newShareModel(t, b, "", false)
	_, cmd := m.dispatchCommand("share", "")
	runShare(t, m, cmd)
	if len(b.inputs) != 0 || m.commandPrompt != nil {
		t.Fatal("shared a conversation that is not saved")
	}
	if n := lastNotice(m); !strings.Contains(n.Text, "Start a conversation") {
		t.Fatalf("notice = %+v", n)
	}
	if got := m.statusRuntime().Sharing; got != "" {
		t.Fatalf("status sharing = %q before the conversation is saved", got)
	}
}

// The share result can arrive before the state event that carries the
// share, so an open /status must follow that event.
func TestStatusFollowsAShareMadeWhileOpen(t *testing.T) {
	b := &tuiShareBackend{spyBackend: spyBackend{t: t, webOrigin: "https://app.datadoghq.com"}}
	m := newShareModel(t, b, forkSourceID, false)
	m.setMode(ModeStatus)
	m.syncStatus()
	cmd := m.share(assistant.ShareConversationInput{ConversationID: forkSourceID, Shared: true})
	m.Update(cmd())
	m.Update(waitEvent(m.events)())
	if view := ansi.Strip(m.status.View()); !strings.Contains(view, "Mar 2, 2030") {
		t.Fatalf("status does not show the share:\n%s", view)
	}
}

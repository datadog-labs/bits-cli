package tui

import (
	"context"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

type tuiRenameBackend struct {
	*resumeBackend
	titles []string
	err    error
	rename func(context.Context) error
}

func (b *tuiRenameBackend) RenameConversation(ctx context.Context, in assistant.RenameConversationInput) error {
	b.titles = append(b.titles, in.Title)
	if b.rename != nil {
		return b.rename(ctx)
	}
	return b.err
}

func newRenameModel(t *testing.T, b *tuiRenameBackend, conversationID string) *Model {
	t.Helper()
	e := agent.New(b, agent.Config{ConversationID: conversationID})
	t.Cleanup(e.Close)
	m := New(e)
	m.setMode(ModeChat)
	t.Cleanup(m.Detach)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

// applyRename runs the rename request cmd starts and applies its result.
func applyRename(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("no rename request was started")
	}
	for _, msg := range flattenMsgs(t, cmd) {
		if msg, ok := msg.(renameResultMsg); ok {
			m.Update(msg)
			return
		}
	}
	t.Fatal("the command did not rename the conversation")
}

func lastNotice(m *Model) chat.Notice {
	if len(m.notices) == 0 {
		return chat.Notice{}
	}
	return m.notices[len(m.notices)-1].Notice
}

func TestRenameWithArgumentKeepsCase(t *testing.T) {
	b := &tuiRenameBackend{resumeBackend: newResumeBackend()}
	m := newRenameModel(t, b, forkSourceID)
	setConversationInput(m, "/rename  Prod  Outage ")
	_, cmd := m.submit()
	applyRename(t, m, cmd)
	if len(b.titles) != 1 || b.titles[0] != "Prod Outage" {
		t.Fatalf("titles = %q", b.titles)
	}
	if n := lastNotice(m); n.Level != chat.NoticeInfo || !strings.Contains(n.Text, "Prod Outage") {
		t.Fatalf("notice = %+v", n)
	}
}

func TestRenameWithoutArgumentAsksForTitle(t *testing.T) {
	b := &tuiRenameBackend{resumeBackend: newResumeBackend()}
	m := newRenameModel(t, b, forkSourceID)
	m.dispatchCommand("rename", "")
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.focus() != focusPrompt || !m.frame.replaced {
		t.Fatalf("focus = %v, replaced = %v", m.focus(), m.frame.replaced)
	}
	m.Update(tea.PasteMsg{Content: "Prod outage"})
	if m.editor.Value() != "" {
		t.Fatal("the paste reached the composer")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.commandPrompt != nil || m.focus() != focusEditor {
		t.Fatal("the prompt stayed open after submitting")
	}
	applyRename(t, m, cmd)
	if len(b.titles) != 1 || b.titles[0] != "Prod outage" {
		t.Fatalf("titles = %q", b.titles)
	}
}

func TestRenamePromptDismissal(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: 'x', Mod: tea.ModCtrl},
	} {
		b := &tuiRenameBackend{resumeBackend: newResumeBackend()}
		m := newRenameModel(t, b, forkSourceID)
		m.dispatchCommand("rename", "")
		m.Update(tea.PasteMsg{Content: "draft"})
		_, cmd := m.Update(key)
		for _, msg := range flattenMsgs(t, cmd) {
			if _, ok := msg.(renameResultMsg); ok {
				t.Fatalf("%s renamed the conversation", key)
			}
		}
		if m.commandPrompt != nil || m.focus() != focusEditor || len(b.titles) != 0 {
			t.Fatalf("%s: prompt open = %v, titles = %q", key, m.commandPrompt != nil, b.titles)
		}
	}
}

func TestRenameRejections(t *testing.T) {
	b := &tuiRenameBackend{resumeBackend: newResumeBackend()}
	m := newRenameModel(t, b, "")
	if _, cmd := m.dispatchCommand("rename", "Title"); cmd != nil || m.commandPrompt != nil {
		t.Fatal("renamed without a conversation")
	}
	if n := lastNotice(m); !strings.Contains(n.Text, "Start a conversation") {
		t.Fatalf("notice = %+v", n)
	}

	m = newRenameModel(t, b, forkSourceID)
	if _, cmd := m.dispatchCommand("rename", strings.Repeat("é", maxTitleLength+1)); cmd != nil {
		t.Fatal("sent a title over the limit")
	}
	if n := lastNotice(m); n.Level != chat.NoticeWarn || !strings.Contains(n.Text, "1 to 200") {
		t.Fatalf("notice = %+v", n)
	}

	m.op = operation{kind: opTurn, run: 1}
	if _, cmd := m.dispatchCommand("rename", "Title"); cmd != nil {
		t.Fatal("renamed during a turn")
	}
	if len(b.titles) != 0 {
		t.Fatalf("titles = %q", b.titles)
	}
}

func TestRenameFailureNotice(t *testing.T) {
	b := &tuiRenameBackend{resumeBackend: newResumeBackend()}
	b.err = &assistant.APIError{
		StatusCode: http.StatusNotFound,
		Input:      assistant.RenameConversationInput{ConversationID: forkSourceID},
	}
	m := newRenameModel(t, b, forkSourceID)
	_, cmd := m.dispatchCommand("rename", "Title")
	applyRename(t, m, cmd)
	if n := lastNotice(m); n.Level != chat.NoticeError || !strings.Contains(n.Text, "Couldn't rename conversation") {
		t.Fatalf("notice = %+v", n)
	}
}

func TestRenameSerializesThroughCancellationAndCompletion(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	b := &tuiRenameBackend{resumeBackend: newResumeBackend(), rename: func(ctx context.Context) error {
		close(started)
		<-release // Even a slow cancellation must keep the operation occupied.
		return ctx.Err()
	}}
	m := newRenameModel(t, b, forkSourceID)
	m.chatPhase = chat.PhaseError
	cmd := m.startRename("First")
	generation := m.op.gen
	if !m.op.busy() {
		t.Fatal("rename was not reserved before command execution")
	}
	for _, name := range []string{"rename", "fork", "resume"} {
		if _, next := m.dispatchCommand(name, "Second"); next != nil || m.op.gen != generation {
			t.Fatalf("/%s overlapped the pending rename", name)
		}
	}
	setConversationInput(m, "next question")
	if _, next := m.submit(); next != nil || m.editor.Value() != "next question" {
		t.Fatal("turn started or draft was lost during rename")
	}
	results := make(chan tea.Msg, 1)
	go func() { results <- cmd() }()
	waitSignal(t, started)
	m.cancelOperation()
	m.cancelOperation()
	if next := m.startRename("Second"); next != nil || m.op.gen != generation {
		t.Fatal("cancellation released admission before completion")
	}
	close(release)
	msg := (<-results).(renameResultMsg)
	m.applyRenameResult(msg)
	if m.op.busy() || m.chatPhase != chat.PhaseError || len(b.titles) != 1 {
		t.Fatal("rename completion did not preserve independent chat state")
	}
	// A duplicate completion must not end a newer operation.
	b.rename = nil
	next := m.startRename("Second")
	m.applyRenameResult(msg)
	if !m.op.busy() || m.op.gen == generation {
		t.Fatal("old rename completion released the newer operation")
	}
	applyRename(t, m, next)
}

func TestRenameCompletionRespectsConversationAndFollowUps(t *testing.T) {
	for _, action := range []string{"new", "logout", "epoch"} {
		t.Run(action, func(t *testing.T) {
			b := &tuiRenameBackend{resumeBackend: newResumeBackend()}
			m := newRenameModel(t, b, forkSourceID)
			// The server has answered, but Update has not seen the completion yet.
			msg := m.startRename("Old title")().(renameResultMsg)
			switch action {
			case "new":
				m.dispatchCommand("new", "")
			case "logout":
				m.logout = func(context.Context) (bool, error, error) { return true, nil, nil }
				m.dispatchCommand("logout", "")
			case "epoch":
				m.conversationEpoch++
			}
			follow := m.applyRenameResult(msg)
			for _, n := range m.notices {
				if strings.Contains(n.Notice.Text, "Renamed conversation") {
					t.Fatal("rename outcome leaked past its owning conversation or cancellation")
				}
			}
			if action == "logout" {
				if follow == nil || m.op.kind != opLogout {
					t.Fatal("rename did not drain into logout")
				}
				m.applyLogoutResult(follow().(logoutResultMsg))
				if !m.loggedOut {
					t.Fatal("deferred logout did not complete")
				}
			} else if m.op.busy() || (action == "new" && m.state.ConversationID != "") {
				t.Fatal("stale UI ownership prevented operation cleanup or reset")
			}
		})
	}
}

func TestCanceledQueuedRenameDoesNotReachBackend(t *testing.T) {
	b := &tuiRenameBackend{resumeBackend: newResumeBackend()}
	m := newRenameModel(t, b, forkSourceID)
	cmd := m.startRename("Title")
	m.requestNewConversation()
	applyRename(t, m, cmd)
	if len(b.titles) != 0 || m.op.busy() || m.state.ConversationID != "" {
		t.Fatal("queued rename executed after cancellation or failed to drain")
	}
}

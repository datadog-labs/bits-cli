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
}

func (b *tuiRenameBackend) RenameConversation(_ context.Context, in assistant.RenameConversationInput) error {
	b.titles = append(b.titles, in.Title)
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

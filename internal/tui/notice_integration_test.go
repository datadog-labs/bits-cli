package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

func TestFakeTurnAndCommandsRenderLocalMessagesInOrder(t *testing.T) {
	backend := fake.New()
	backend.ScriptRoot = filepath.Join("..", "..")
	m := New(agent.New(backend, assistant.SendOptions{}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.Update(tea.PasteMsg{Content: `load("fakedata/notices.star", "rate_limit"); rate_limit()`})
	_, _ = m.submit()
	for m.op.busy() {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.op.gen, m.op.events)))
	}
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
	if len(m.transcript.Blocks) != 2 || len(m.notices) != 3 {
		t.Fatalf("agent blocks=%d local messages=%d", len(m.transcript.Blocks), len(m.notices))
	}
	_, _ = m.dispatchCommand("new", "")
	if len(m.transcript.Blocks) != 0 || len(m.notices) != 1 || latestNotice(m).Text != "Started a new conversation." {
		t.Fatalf("new conversation retained old messages: blocks=%d notices=%+v", len(m.transcript.Blocks), m.notices)
	}
}

func TestDelayedBrowserResultStaysOutOfNewConversation(t *testing.T) {
	m := New(agent.New(fake.New(), assistant.SendOptions{}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	oldEpoch := m.conversationEpoch
	m.startNewConversation()
	_, _ = m.Update(webOpenResultMsg{epoch: oldEpoch, url: "https://example.com/old"})
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
			m := New(agent.New(fake.New(), assistant.SendOptions{}))
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
	m := New(agent.New(fake.New(), assistant.SendOptions{}))
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

func latestNotice(m *Model) chat.Notice {
	if len(m.notices) == 0 {
		return chat.Notice{}
	}
	return m.notices[len(m.notices)-1].Notice
}

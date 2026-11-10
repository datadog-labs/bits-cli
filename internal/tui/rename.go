package tui

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

// maxTitleLength is the longest title the server accepts, in characters.
const maxTitleLength = 200

const renameTimeout = 30 * time.Second

type renameResultMsg struct {
	title string
	err   error
}

// startRename renames the current conversation to argument, or asks for a
// title when there is none.
func (m *Model) startRename(argument string) tea.Cmd {
	conversationID := m.state.ConversationID
	if !agent.ValidConversationID(conversationID) {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Start a conversation before using /rename."))
	}
	if argument != "" {
		return m.rename(conversationID, argument)
	}
	form := components.NewTextPrompt("Rename conversation", "", maxTitleLength)
	form.SetPlaceholder("New title")
	m.openCommandPrompt(form, func(title string) tea.Cmd {
		return m.rename(conversationID, title)
	})
	return nil
}

// rename saves title in the background and reports the outcome as a notice.
func (m *Model) rename(conversationID, title string) tea.Cmd {
	title = strings.TrimSpace(title)
	if n := utf8.RuneCountInString(title); n == 0 || n > maxTitleLength {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Conversation titles must be 1 to %d characters.", maxTitleLength))
	}
	engine := m.engine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), renameTimeout)
		defer cancel()
		return renameResultMsg{title: title, err: engine.RenameConversation(ctx, conversationID, title)}
	}
}

func (m *Model) applyRenameResult(msg renameResultMsg) tea.Cmd {
	if msg.err != nil {
		return m.postNotice(noticeForError("could not rename conversation", msg.err))
	}
	return m.postNotice(notice(chat.NoticeInfo, nil, "Renamed conversation to “%s”.", escape.SingleLine(msg.title)))
}

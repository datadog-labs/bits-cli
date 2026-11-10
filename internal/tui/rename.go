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
	generation uint64
	epoch      uint64
	title      string
	err        error
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
	epoch := m.conversationEpoch
	m.openCommandPrompt(form, func(title string) tea.Cmd {
		if epoch != m.conversationEpoch {
			return nil
		}
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
	if conversationID != m.state.ConversationID {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), renameTimeout)
	if !m.begin(opRename, cancel) {
		return nil
	}
	generation, epoch := m.op.gen, m.conversationEpoch
	engine := m.engine
	return func() tea.Msg {
		// Cancellation may precede execution of this queued command.
		err := ctx.Err()
		if err == nil {
			err = engine.RenameConversation(ctx, conversationID, title)
		}
		return renameResultMsg{generation: generation, epoch: epoch, title: title, err: err}
	}
}

func (m *Model) applyRenameResult(msg renameResultMsg) tea.Cmd {
	if !m.op.matches(opRename, msg.generation) {
		return nil
	}
	interrupted := m.op.interrupted()
	cmd := m.endOperation()
	// Completion may have performed a queued reset. Bookkeeping must finish
	// even when the result no longer belongs in the visible transcript.
	if interrupted || msg.epoch != m.conversationEpoch {
		return cmd
	}
	if msg.err != nil {
		m.postNotice(noticeForError("could not rename conversation", msg.err))
	} else {
		m.postNotice(notice(chat.NoticeInfo, nil, "Renamed conversation to “%s”.", escape.SingleLine(msg.title)))
	}
	return cmd
}

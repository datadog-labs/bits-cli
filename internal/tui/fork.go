package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

type forkResultMsg struct {
	generation   uint64
	ctx          context.Context
	targetID     string
	conversation *agent.Conversation
	err          error
}

func (m *Model) startFork(argument string) tea.Cmd {
	if argument != "" {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Usage: /fork (or /branch)"))
	}
	if m.op.busy() || m.conversationTask.running() || m.picker != nil {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Wait for the current operation to finish before forking."))
	}
	sourceID := m.state.ConversationID
	if !agent.ValidConversationID(sourceID) {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Start a conversation before using /fork."))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	m.begin(opFork, 0, nil, cancel)
	m.chatPhase = chat.PhaseLoading
	generation := m.op.gen
	engine := m.engine
	closeSearch := m.stopCompletionSearches()
	return func() tea.Msg {
		if closeSearch != nil {
			_ = closeSearch()
		}
		result := forkResultMsg{generation: generation, ctx: ctx}
		createCtx, createCancel := context.WithTimeout(ctx, 60*time.Second)
		summary, err := engine.ForkConversation(createCtx, sourceID)
		createCancel()
		if summary != nil {
			result.targetID = summary.ConversationID
		}
		if err == nil {
			readCtx, readCancel := context.WithTimeout(ctx, historyLoadTimeout)
			result.conversation, err = engine.LoadConversation(readCtx, result.targetID)
			readCancel()
		}
		result.err = err
		return result
	}
}

func (m *Model) applyForkResult(msg forkResultMsg) tea.Cmd {
	if m.op.kind != opFork || msg.generation != m.op.gen {
		return nil
	}
	err := msg.err
	if err == nil {
		err = msg.ctx.Err()
	}
	if err == nil && m.op.stop != stopAll && m.op.then == thenNothing {
		err = m.engine.InstallConversation(msg.ctx, msg.conversation)
		if err == nil {
			m.state = m.engine.State()
			m.conversationEpoch++
			m.selection.clear()
			m.notices = nil
			m.list.ReplaceTranscript(m.state.Blocks, m.state.Tasks, m.transcriptNotices())
		}
	} else if err == nil {
		err = context.Canceled
	}
	// Finish queued resets before publishing the outcome of the remote mutation.
	cmd := m.endOperation()
	if err != nil {
		if msg.targetID != "" {
			m.postNotice(conversationErrorNotice("could not load fork", err))
			m.postNotice(notice(chat.NoticeWarn, nil, "Fork %s was created. Open it through /resume; running /fork again creates another copy.", msg.targetID))
		} else if definitiveForkFailure(err) {
			m.postNotice(conversationErrorNotice("could not create fork", err))
		} else {
			m.postNotice(notice(chat.NoticeWarn, err, "The fork was not confirmed. Check /resume before retrying; a copy may have been created."))
		}
	}
	return cmd
}

// transcriptNotices derives the persisted fork marker independently of local
// notices, so restore, resume and a new fork all use the same boundary.
func (m *Model) transcriptNotices() []chat.NoticeItem {
	fork := m.state.Fork
	if fork.After == (agent.BlockID{}) {
		return m.notices
	}
	source := fork.SourceID
	if fork.SourceTitle != "" {
		source = escape.SingleLine(fork.SourceTitle) + " (" + source + ")"
	}
	marker := chat.NoticeItem{After: fork.After, Notice: notice(chat.NoticeFork, nil, "Forked conversation · %s\nOriginal: %s", m.state.ConversationID, source)}
	return append([]chat.NoticeItem{marker}, m.notices...)
}

func definitiveForkFailure(err error) bool {
	var apiErr *assistant.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case 400, 401, 403, 404, 409, 413, 422:
		return true
	default:
		return false
	}
}

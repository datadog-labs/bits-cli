package tui

import (
	"context"
	"errors"
	"math"
	"slices"
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

// transcriptNotices derives the persisted fork marker and the per-response
// "Done" lines independently of local notices, then merges all three streams
// by anchor position in m.state.Blocks. buildPresentationWithNotices requires
// notices in non-decreasing block order and silently re-anchors anything out
// of order to the wrong block.
func (m *Model) transcriptNotices() []chat.NoticeItem {
	blocks := m.state.Blocks
	index := make(map[agent.BlockID]int, len(blocks))
	for i, b := range blocks {
		index[b.ID] = i
	}
	// anchorIndex turns an After block into a sort key: right after that
	// block, before everything for the zero BlockID, or past the end when
	// the block no longer survives — buildPresentationWithNotices drops or
	// clamps that case downstream exactly as it did before this merge.
	anchorIndex := func(after agent.BlockID) int {
		if after == (agent.BlockID{}) {
			return 0
		}
		if i, ok := index[after]; ok {
			return i + 1
		}
		return len(blocks) + 1
	}

	var all []chat.NoticeItem

	if fork := m.state.Fork; fork.After != (agent.BlockID{}) {
		source := fork.SourceID
		if fork.SourceTitle != "" {
			source = escape.SingleLine(fork.SourceTitle) + " (" + source + ")"
		}
		all = append(all, chat.NoticeItem{After: fork.After, Notice: notice(chat.NoticeFork, nil, "Forked conversation · %s\nOriginal: %s", m.state.ConversationID, source)})
	}
	// Done lines go in before m.notices: a run's finish chronologically
	// precedes anything posted after it, and postNotice anchors to the same
	// last block a just-finished run anchors to (fork.go:63-67), so on a tied
	// anchor the done line must sort first to stay directly under its
	// response rather than drifting below a later notice like "Copied to
	// clipboard." SortStableFunc keeps this insertion order on ties, so no
	// extra tie-breaking key is needed.
	//
	// Done lines are keyed from the top of the uint64 ID space: nextNoticeID
	// climbs from 1 and the fork marker sits at the zero value, so reserving
	// the high end means any number of done lines can never collide with
	// either of those, or with each other.
	for i, ts := range m.responseTimestamps() {
		all = append(all, chat.NoticeItem{
			ID:     math.MaxUint64 - uint64(i),
			After:  ts.After,
			Notice: notice(chat.NoticeDone, nil, "Done · %s", chat.FormatFinishedAt(ts.At)),
		})
	}
	all = append(all, m.notices...)

	slices.SortStableFunc(all, func(a, b chat.NoticeItem) int {
		return anchorIndex(a.After) - anchorIndex(b.After)
	})
	return all
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

package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	conversationview "github.com/datadog-labs/bits-cli/internal/tui/conversations"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

// requestNewConversation defers the reset until the current engine channel is
// closed. Cancelling only once makes repeated /new or /clear submissions
// idempotent and keeps cleanup from cancelling a second time.
func (m *Model) requestNewConversation() tea.Cmd {
	m.after(thenNewConversation)
	return m.stopCompletionSearches()
}

// startNewConversation resets only conversation-scoped state. It deliberately
// leaves the editor object (and its prompt-history source), styles, backend
// client, and process-wide configuration intact; resetting the editor only
// ends any history browsing.
func (m *Model) startNewConversation() tea.Cmd {
	if err := m.engine.NewConversation(); err != nil {
		m.postNotice(noticeForError("could not start a new conversation", err))
		m.list.ScrollToBottom()
		return nil
	}

	// A reset is a new event identity domain even though active work was drained.
	// This makes any delayed Bubble Tea message from the prior domain harmless.
	m.op.gen++
	m.conversationEpoch++
	m.selection.clear()
	m.transcript = agent.TranscriptSnapshot{}
	m.list.Reset()
	m.convID = ""
	m.usage = nil
	m.chatPhase = chat.PhaseIdle
	m.editor.Reset()
	closeFileSearch := m.stopCompletionSearches()
	m.notices = nil
	m.setMode(ModeChat)
	m.postNotice(notice(chat.NoticeInfo, nil, "Started a new conversation."))
	m.list.ScrollToBottom()
	return closeFileSearch // Update reconciles focus and animations after the mode change.
}

type conversationListResultMsg struct {
	generation uint64
	result     agent.ConversationListResult
}

type conversationSwitchResultMsg struct {
	generation   uint64
	ctx          context.Context
	conversation *agent.Conversation
	err          error
}

func (m *Model) openConversationPicker() tea.Cmd {
	picker := conversationview.New(m.width, m.height, m.styles)
	m.picker = &picker
	m.setMode(ModeConversations)
	return m.startConversationList()
}

func (m *Model) startConversationList() tea.Cmd {
	if m.picker == nil {
		return nil
	}
	m.picker.SetLoading(conversationview.OperationList)
	m.conversationSwitchID = ""
	ctx, generation := m.conversationTask.start(context.Background(), historyLoadTimeout)
	engine := m.engine
	return func() tea.Msg {
		return conversationListResultMsg{generation: generation, result: engine.ListConversations(ctx)}
	}
}

func (m *Model) applyConversationListResult(msg conversationListResultMsg) tea.Cmd {
	if msg.generation != m.conversationTask.gen || m.picker == nil {
		return nil
	}
	m.conversationTask.done()
	if msg.result.Err != nil {
		n := conversationErrorNotice("could not load conversations", msg.result.Err)
		m.picker.SetError(n.Text, msg.result.Err)
		return nil
	}
	cmd := m.picker.SetConversations(msg.result.Conversations)
	if msg.result.Omitted > 0 {
		m.picker.SetWarning(fmt.Sprintf("%d malformed conversation records omitted", msg.result.Omitted))
	}
	return cmd
}

func (m *Model) selectConversation(summary assistant.ConversationSummary) tea.Cmd {
	if m.picker == nil || m.picker.State() != conversationview.StateReady {
		return nil
	}
	return m.startConversationSwitch(strings.TrimSpace(summary.ConversationID))
}

func (m *Model) startConversationSwitch(conversationID string) tea.Cmd {
	if m.picker == nil {
		return nil
	}
	if conversationID == m.convID {
		return m.closeConversationPicker()
	}
	m.picker.SetLoading(conversationview.OperationOpen)
	m.conversationSwitchID = conversationID
	ctx, generation := m.conversationTask.start(context.Background(), historyLoadTimeout)
	engine := m.engine
	return func() tea.Msg {
		conversation, err := engine.LoadConversation(ctx, conversationID)
		return conversationSwitchResultMsg{generation: generation, ctx: ctx, conversation: conversation, err: err}
	}
}

func (m *Model) applyConversationSwitchResult(msg conversationSwitchResultMsg) tea.Cmd {
	if msg.generation != m.conversationTask.gen || m.picker == nil {
		return nil
	}
	defer m.conversationTask.done()
	// Install before changing the visible state; a failed or expired load
	// leaves both the engine and the UI on the old conversation.
	err := msg.err
	if err == nil {
		err = m.engine.InstallConversation(msg.ctx, msg.conversation)
	}
	if err != nil {
		n := conversationErrorNotice("resume failed", err)
		m.picker.SetError(n.Text, err)
		return m.postNotice(n)
	}
	m.transcript = agent.TranscriptSnapshot{Blocks: m.engine.Snapshot()}
	m.conversationEpoch++
	m.convID = m.engine.ConversationID()
	m.usage = nil
	m.chatPhase = chat.PhaseIdle
	m.list.Reset()
	m.notices = nil
	m.syncTranscript()
	m.list.ScrollToBottom()
	m.dropConversationPicker()
	return m.resumePendingTools() // Update reconciles focus and animations after the mode change.
}

func (m *Model) retryConversationOperation() tea.Cmd {
	if m.picker == nil || m.picker.State() != conversationview.StateError {
		return nil
	}
	switch m.picker.Operation() {
	case conversationview.OperationOpen:
		return m.startConversationSwitch(m.conversationSwitchID)
	case conversationview.OperationList:
		return m.startConversationList()
	default:
		return nil
	}
}

// closeConversationPicker invalidates pending reads and returns immediately.
func (m *Model) closeConversationPicker() tea.Cmd {
	if m.picker == nil {
		return nil
	}
	m.conversationTask.stop()
	m.dropConversationPicker()
	return nil // Update reconciles editor focus after the mode change.
}

// dropConversationPicker returns to chat. The picker is non-nil exactly while
// the mode is ModeConversations.
func (m *Model) dropConversationPicker() {
	m.picker = nil
	m.conversationSwitchID = ""
	m.setMode(ModeChat)
}

func (m *Model) updateConversationPicker(msg tea.Msg) tea.Cmd {
	if m.picker == nil {
		return nil
	}
	next, cmd := m.picker.Update(msg)
	*m.picker = next
	return cmd
}

func conversationErrorNotice(operation string, err error) chat.Notice {
	n := noticeForError(operation, err)
	n.Text = ansi.Truncate(escape.SingleLine(n.Text), 240, "…")
	n.Err = nil // picker messages are kept as curated text
	return n
}

// resumeSelectedConversation loads a conversation chosen from the startup
// offer. It drives the switch through the /resume picker so the existing
// loading, error and retry paths apply unchanged.
func (m *Model) resumeSelectedConversation(conversationID string) tea.Cmd {
	picker := conversationview.New(m.width, m.height, m.styles)
	for _, summary := range m.resume.conversations {
		if summary.ConversationID == conversationID {
			picker.SetConversations([]assistant.ConversationSummary{summary})
			break
		}
	}
	m.picker = &picker
	m.setMode(ModeConversations)
	return m.startConversationSwitch(conversationID)
}

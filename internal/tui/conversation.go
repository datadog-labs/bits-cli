package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	"github.com/DataDog/bits-cli/internal/tui/escape"
)

// requestNewConversation defers the reset until the current engine channel is
// closed. cancelRemote makes repeated /new or /clear submissions idempotent and
// prevents cleanup from invoking cancellation a second time.
func (m *Model) requestNewConversation() tea.Cmd {
	m.pendingNew = true
	m.cancelRemote()
	return m.showNotice(notice(chat.NoticeInfo, nil,
		"Cancelling the current operation before starting a new conversation…"), 0)
}

// startNewConversation resets only conversation-scoped state. It deliberately
// leaves the editor object (and therefore local input history), styles, backend
// client, and process-wide configuration intact.
func (m *Model) startNewConversation() tea.Cmd {
	if err := m.engine.NewConversation(); err != nil {
		return m.showNotice(noticeForError("could not start a new conversation", err), 0)
	}

	// A reset is a new event identity domain even though active work was drained.
	// This makes any delayed Bubble Tea message from the prior domain harmless.
	m.turnGen++
	m.blocks = nil
	m.list.Reset()
	m.convID = ""
	m.usage = nil
	m.chatPhase = chat.PhaseIdle
	m.pendingNew = false
	m.editor.Reset()
	m.stopEntitySearch()
	m.clearNotice()
	m.setMode(ModeChat)
	m.list.ScrollToBottom()
	return nil // Update reconciles editor focus after the mode change.
}

type conversationRetry int

const (
	retryNone conversationRetry = iota
	retryList
	retrySwitch
)

type conversationListResultMsg struct {
	generation uint64
	result     agent.ConversationListResult
}

type conversationSwitchResultMsg struct {
	generation uint64
	result     agent.ConversationSwitchResult
}

func waitConversationList(generation uint64, results <-chan agent.ConversationListResult) tea.Cmd {
	return func() tea.Msg {
		result, ok := <-results
		if !ok {
			result.Err = errors.New("conversation list closed without a result")
		}
		return conversationListResultMsg{generation: generation, result: result}
	}
}

func waitConversationSwitch(generation uint64, results <-chan agent.ConversationSwitchResult) tea.Cmd {
	return func() tea.Msg {
		result, ok := <-results
		if !ok {
			result.Err = errors.New("conversation load closed without a result")
		}
		return conversationSwitchResultMsg{generation: generation, result: result}
	}
}

func (m *Model) openConversationPicker() tea.Cmd {
	m.clearNotice()
	m.conversationClosing = false
	picker := conversationview.New(m.width, m.height, m.styles)
	m.picker = &picker
	m.setMode(ModeConversations)
	return m.startConversationList()
}

func (m *Model) startConversationList() tea.Cmd {
	if m.picker == nil {
		return nil
	}
	m.invalidateConversationOperation()
	m.picker.SetLoading(conversationview.OperationList)
	m.conversationRetry = retryList
	m.conversationSwitchID = ""
	generation := m.conversationGeneration
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	m.conversationCancel = cancel
	return waitConversationList(generation, m.engine.ListConversations(ctx))
}

func (m *Model) applyConversationListResult(msg conversationListResultMsg) tea.Cmd {
	if msg.generation != m.conversationGeneration {
		return nil
	}
	if m.conversationClosing {
		m.finishConversationOperation()
		return m.finishClosingConversationPicker()
	}
	if m.picker == nil || m.mode != ModeConversations {
		return nil
	}
	m.finishConversationOperation()
	if msg.result.Err != nil {
		m.conversationRetry = retryList
		n := conversationErrorNotice("could not load conversations", msg.result.Err)
		m.picker.SetError(n.Text, msg.result.Err)
		return nil
	}
	m.conversationRetry = retryNone
	cmd := m.picker.SetConversations(msg.result.Conversations)
	if msg.result.Omitted > 0 {
		m.picker.SetWarning(fmt.Sprintf("%d malformed conversation records omitted", msg.result.Omitted))
	}
	return cmd
}

func (m *Model) selectConversation(summary assistant.ConversationSummary) tea.Cmd {
	if m.picker == nil || m.mode != ModeConversations || m.conversationClosing || m.picker.State() != conversationview.StateReady {
		return nil
	}
	return m.startConversationSwitch(strings.TrimSpace(summary.ConversationID))
}

func (m *Model) startConversationSwitch(conversationID string) tea.Cmd {
	if m.picker == nil {
		return nil
	}
	m.invalidateConversationOperation()
	m.picker.SetLoading(conversationview.OperationOpen)
	m.conversationRetry = retrySwitch
	m.conversationSwitchID = conversationID
	generation := m.conversationGeneration
	ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
	m.conversationCancel = cancel
	return waitConversationSwitch(generation, m.engine.SwitchConversation(ctx, conversationID))
}

func (m *Model) applyConversationSwitchResult(msg conversationSwitchResultMsg) tea.Cmd {
	if msg.generation != m.conversationGeneration {
		_ = msg.result.Discard()
		return nil
	}
	if m.conversationClosing {
		_ = msg.result.Discard()
		m.finishConversationOperation()
		return m.finishClosingConversationPicker()
	}
	if m.picker == nil || m.mode != ModeConversations {
		_ = msg.result.Discard()
		return nil
	}
	if msg.result.Err != nil {
		m.finishConversationOperation()
		m.conversationRetry = retrySwitch
		n := conversationErrorNotice("resume failed", msg.result.Err)
		m.picker.SetError(n.Text, msg.result.Err)
		return m.showNotice(n, 0)
	}
	if strings.TrimSpace(msg.result.ConversationID) == "" {
		_ = msg.result.Discard()
		m.finishConversationOperation()
		err := errors.New("conversation load returned no conversation id")
		m.conversationRetry = retrySwitch
		n := conversationErrorNotice("resume failed", err)
		m.picker.SetError(n.Text, err)
		return m.showNotice(n, 0)
	}

	// Commit engine state first; only a successful commit changes the visible
	// root state. Cancel/deadline failure therefore leaves both views on old data.
	if err := msg.result.Commit(); err != nil {
		m.finishConversationOperation()
		m.conversationRetry = retrySwitch
		n := conversationErrorNotice("resume failed", err)
		m.picker.SetError(n.Text, err)
		return m.showNotice(n, 0)
	}
	m.finishConversationOperation()
	m.blocks = append([]agent.Block(nil), msg.result.Blocks...)
	m.convID = msg.result.ConversationID
	m.usage = nil
	m.chatPhase = chat.PhaseIdle
	m.list.Reset()
	m.list.SetItems(m.blocks)
	m.list.ScrollToBottom()
	m.clearNotice()
	m.conversationRetry = retryNone
	m.conversationSwitchID = ""
	m.conversationClosing = false
	m.picker = nil
	m.setMode(ModeChat)
	return nil // Update reconciles editor focus after the mode change.
}

func (m *Model) retryConversationOperation() tea.Cmd {
	if m.picker == nil || m.mode != ModeConversations || m.conversationClosing || m.picker.State() != conversationview.StateError {
		return nil
	}
	switch m.conversationRetry {
	case retrySwitch:
		return m.startConversationSwitch(m.conversationSwitchID)
	case retryList:
		return m.startConversationList()
	default:
		return nil
	}
}

func (m *Model) closeConversationPicker() tea.Cmd {
	if m.mode != ModeConversations {
		return nil
	}
	if m.conversationClosing {
		return nil
	}
	if m.conversationCancel != nil {
		m.conversationClosing = true
		m.picker.SetClosing()
		m.conversationCancel()
		return nil
	}
	return m.finishClosingConversationPicker()
}

func (m *Model) finishClosingConversationPicker() tea.Cmd {
	m.picker = nil
	m.conversationClosing = false
	m.conversationRetry = retryNone
	m.conversationSwitchID = ""
	m.setMode(ModeChat)
	return nil // Update reconciles editor focus after the mode change.
}

// abandonConversationPicker is the process-exit path. Unlike ordinary Escape,
// quitting does not resume chat input, so the UI need not wait for the canceled
// result before disappearing. The result still drains through its waiter and
// stale-result handling.
func (m *Model) abandonConversationPicker() {
	m.invalidateConversationOperation()
	m.picker = nil
	m.conversationClosing = false
	m.conversationRetry = retryNone
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

func (m *Model) invalidateConversationOperation() {
	m.conversationGeneration++
	m.finishConversationOperation()
}

func (m *Model) finishConversationOperation() {
	if m.conversationCancel != nil {
		m.conversationCancel()
		m.conversationCancel = nil
	}
}

func conversationErrorNotice(operation string, err error) chat.Notice {
	n := noticeForError(operation, err)
	n.Text = ansi.Truncate(escape.SingleLine(n.Text), 240, "…")
	n.Err = nil // never append raw backend detail in the notice bar
	return n
}

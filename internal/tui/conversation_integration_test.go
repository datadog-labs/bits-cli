package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
)

const resumeConversationID = "87654321-4321-4321-4321-210987654321"

type resumeBackend struct {
	lists     chan resumeListReply
	histories chan resumeHistoryReply
}

type resumeListReply struct {
	response *assistant.UserConversationsResponse
	err      error
}
type resumeHistoryReply struct {
	response *assistant.ConversationHistoryResponse
	err      error
}

func (b *resumeBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	return "", errors.New("unexpected Send")
}
func (b *resumeBackend) UserConversations(ctx context.Context) (*assistant.UserConversationsResponse, error) {
	select {
	case reply := <-b.lists:
		return reply.response, reply.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (b *resumeBackend) ConversationHistory(ctx context.Context, _ assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	select {
	case reply := <-b.histories:
		return reply.response, reply.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newResumeBackend() *resumeBackend {
	return &resumeBackend{lists: make(chan resumeListReply, 4), histories: make(chan resumeHistoryReply, 4)}
}

func summaries(values ...assistant.ConversationSummary) *assistant.UserConversationsResponse {
	r := &assistant.UserConversationsResponse{}
	r.Data.Type = "user-conversations-response"
	for i := range values {
		values[i].ID = values[i].ConversationID
	}
	r.Data.Attributes.Conversations = values
	return r
}

func history(messages ...assistant.Message) *assistant.ConversationHistoryResponse {
	r := &assistant.ConversationHistoryResponse{}
	r.Data.Type = "conversation-history-response"
	r.Data.Attributes.Messages = messages
	return r
}

func runResumeCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		return msg
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
		return nil
	}
}

func textBlock(id, text string) agent.Block {
	return agent.Block{
		ID:       agent.BlockID{Scope: agent.ScopeMessage, Key: id, Kind: assistant.KindText},
		Role:     assistant.RoleAssistant,
		Kind:     assistant.KindText,
		Markdown: &assistant.MarkdownPayload{Content: text},
		Complete: true,
	}
}

func TestResumeListErrorRetryEmptyAndCancelPreserveChat(t *testing.T) {
	backend := newResumeBackend()
	m := New(agent.New(backend, assistant.SendOptions{ConversationID: "old"}))
	m.resize(50, 12)
	m.blocks = []agent.Block{textBlock("same-message-id", "OLD")}
	m.refreshViewport()
	_ = m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "draft survives"})

	backend.lists <- resumeListReply{err: errors.New("list unavailable")}
	wait := m.openConversationPicker()
	_, _ = m.Update(runResumeCmd(t, wait))
	if m.picker.State() != conversationview.StateError || m.convID != "old" {
		t.Fatalf("list failure state = %v id=%q", m.picker.State(), m.convID)
	}

	backend.lists <- resumeListReply{response: summaries()}
	_, wait = m.Update(conversationview.RetryMsg{})
	_, _ = m.Update(runResumeCmd(t, wait))
	if m.picker.State() != conversationview.StateEmpty {
		t.Fatalf("retry state = %v", m.picker.State())
	}
	_, _ = m.Update(conversationview.CancelledMsg{})
	if m.mode != ModeChat || m.picker != nil || m.convID != "old" || m.editor.Value() != "draft survives" || len(m.blocks) != 1 {
		t.Fatalf("cancel changed chat: mode=%v picker=%v id=%q draft=%q blocks=%d", m.mode, m.picker != nil, m.convID, m.editor.Value(), len(m.blocks))
	}
}

func TestCtrlCCancelsResumeOperationAndQuits(t *testing.T) {
	backend := newResumeBackend()
	engine := agent.New(backend, assistant.SendOptions{ConversationID: "old"})
	m := New(engine)
	wait := m.openConversationPicker()
	if m.mode != ModeConversations || m.conversationCancel == nil {
		t.Fatalf("resume did not start: mode=%v cancel=%v", m.mode, m.conversationCancel != nil)
	}

	_, quit := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if quit == nil {
		t.Fatal("ctrl+c did not return a quit command")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c command = %T, want tea.QuitMsg", quit())
	}
	if m.mode != ModeChat || m.picker != nil || m.conversationCancel != nil {
		t.Fatalf("ctrl+c left resume active: mode=%v picker=%v cancel=%v", m.mode, m.picker != nil, m.conversationCancel != nil)
	}

	// The waiter must also complete after cancellation; otherwise the engine
	// still owns a leaked resume operation while the application exits.
	msg := runResumeCmd(t, wait)
	result, ok := msg.(conversationListResultMsg)
	if !ok || result.result.Err == nil {
		t.Fatalf("cancelled waiter = %#v", msg)
	}
	if engine.OperationActive() {
		t.Fatal("ctrl+c returned while the engine still owned the resume operation")
	}
}

func TestResumeSwitchFailureRetryThenAtomicSuccess(t *testing.T) {
	backend := newResumeBackend()
	engine := agent.New(backend, assistant.SendOptions{ConversationID: "old"})
	m := New(engine)
	m.resize(50, 12)
	m.blocks = []agent.Block{textBlock("same-message-id", "OLD-CACHED")}
	m.refreshViewport()
	_ = m.list.Render() // populate the old conversation's render cache

	summary := assistant.ConversationSummary{ConversationID: resumeConversationID, UpdatedAt: 10, Title: "New"}
	backend.lists <- resumeListReply{response: summaries(summary)}
	wait := m.openConversationPicker()
	_, _ = m.Update(runResumeCmd(t, wait))

	backend.histories <- resumeHistoryReply{err: errors.New("network down")}
	_, wait = m.Update(conversationview.SelectedMsg{Conversation: summary})
	_, _ = m.Update(runResumeCmd(t, wait))
	if m.picker.State() != conversationview.StateError || m.convID != "old" || engine.ConversationID() != "old" || m.blocks[0].Markdown.Content != "OLD-CACHED" {
		t.Fatalf("failed load mutated state: picker=%v root=%q engine=%q blocks=%+v", m.picker.State(), m.convID, engine.ConversationID(), m.blocks)
	}

	backend.histories <- resumeHistoryReply{response: history(
		assistant.AssistantMessage("same-message-id", assistant.TextContent("NEW")),
		assistant.AssistantMessage("same-message-id", assistant.TextContent("-TRANSCRIPT")),
	)}
	_, wait = m.Update(conversationview.RetryMsg{})
	_, _ = m.Update(runResumeCmd(t, wait))
	if m.mode != ModeChat || m.picker != nil || m.convID != resumeConversationID || engine.ConversationID() != resumeConversationID {
		t.Fatalf("success state: mode=%v picker=%v root=%q engine=%q", m.mode, m.picker != nil, m.convID, engine.ConversationID())
	}
	if len(m.blocks) != 1 || m.blocks[0].Markdown.Content != "NEW-TRANSCRIPT" {
		t.Fatalf("new blocks = %+v", m.blocks)
	}
	if rendered := m.list.Render(); !strings.Contains(rendered, "NEW-TRANSCRIPT") || strings.Contains(rendered, "OLD-CACHED") {
		t.Fatalf("cross-conversation render cache leaked: %q", rendered)
	}
}

func TestCancelLoadedButUnappliedResultCannotSwitchEngine(t *testing.T) {
	backend := newResumeBackend()
	engine := agent.New(backend, assistant.SendOptions{ConversationID: "old"})
	m := New(engine)
	summary := assistant.ConversationSummary{ConversationID: resumeConversationID, Title: "New"}
	backend.lists <- resumeListReply{response: summaries(summary)}
	wait := m.openConversationPicker()
	_, _ = m.Update(runResumeCmd(t, wait))

	backend.histories <- resumeHistoryReply{response: history(assistant.AssistantMessage("m", assistant.TextContent("new")))}
	_, wait = m.Update(conversationview.SelectedMsg{Conversation: summary})
	loadedMsg := runResumeCmd(t, wait)
	// Escape wins the event race: cancellation invalidates this already-loaded
	// result before Bubble Tea applies it.
	_, _ = m.Update(conversationview.CancelledMsg{})
	_, _ = m.Update(loadedMsg)
	if m.mode != ModeChat || m.convID != "old" || engine.ConversationID() != "old" {
		t.Fatalf("stale loaded result switched state: mode=%v root=%q engine=%q", m.mode, m.convID, engine.ConversationID())
	}
}

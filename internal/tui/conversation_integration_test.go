package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	conversationview "github.com/DataDog/bits-cli/internal/tui/conversations"
	"github.com/DataDog/bits-cli/internal/workspace"
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

// halfStreamedWriteBackend streams half of a write_file input on its first
// Send and then waits for cancellation, never sending the final call. Later
// Sends answer with plain text.
type halfStreamedWriteBackend struct{ sends int }

func (b *halfStreamedWriteBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.sends++
	if b.sends > 1 {
		return "conversation", emitMessages(emit, assistant.AssistantMessage("answer", assistant.TextContent("still alive")))
	}
	err := emitMessages(emit,
		assistant.AssistantMessage("write", assistant.Content{
			Type: assistant.ContentToolCallStarted,
			Tool: &assistant.ToolPayload{ToolCallID: "write", ToolName: "write_file", IsClientSide: true},
		}),
		assistant.AssistantMessage("write", assistant.Content{
			Type: assistant.ContentToolCallInputDelta,
			Tool: &assistant.ToolPayload{ToolCallID: "write", PartialJSON: `{"path":"half.txt","content":"alpha\nbravo\nch`},
		}),
	)
	if err != nil {
		return "conversation", err
	}
	<-ctx.Done()
	return "conversation", ctx.Err()
}

func emitMessages(emit func(assistant.AssistantResponse) error, messages ...assistant.Message) error {
	for _, message := range messages {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = message
		if err := emit(response); err != nil {
			return err
		}
	}
	return nil
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
	m.transcript.Blocks = []agent.Block{textBlock("same-message-id", "OLD")}
	m.syncTranscript()
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
	if m.mode != ModeChat || m.picker != nil || m.convID != "old" || m.editor.Value() != "draft survives" || len(m.transcript.Blocks) != 1 {
		t.Fatalf("cancel changed chat: mode=%v picker=%v id=%q draft=%q blocks=%d", m.mode, m.picker != nil, m.convID, m.editor.Value(), len(m.transcript.Blocks))
	}
}

func TestPickerBlursEditorUntilClosed(t *testing.T) {
	backend := newResumeBackend()
	m := New(agent.New(backend, assistant.SendOptions{ConversationID: "old"}))
	m.resize(50, 12)
	_ = m.editor.Focus()
	if !m.editor.Focused() {
		t.Fatal("editor not focused before opening the picker")
	}

	backend.lists <- resumeListReply{response: summaries()}
	wait := m.openConversationPicker()
	_, _ = m.Update(runResumeCmd(t, wait))
	if m.mode != ModeConversations || m.editor.Focused() {
		t.Fatalf("picker did not take focus from the editor: mode=%v focused=%v", m.mode, m.editor.Focused())
	}

	_, _ = m.Update(conversationview.CancelledMsg{})
	if m.mode != ModeChat || !m.editor.Focused() {
		t.Fatalf("closing the picker did not restore editor focus: mode=%v focused=%v", m.mode, m.editor.Focused())
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

func TestCancelPendingResumeWaitsForEngineDrainBeforeReturningToChat(t *testing.T) {
	backend := newResumeBackend()
	engine := agent.New(backend, assistant.SendOptions{ConversationID: "old"})
	m := New(engine)
	m.resize(50, 12)
	_ = m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "draft survives"})
	wait := m.openConversationPicker()

	if cmd := m.closeConversationPicker(); cmd != nil {
		t.Fatal("pending close focused chat before the engine drained")
	}
	if m.mode != ModeConversations || m.picker == nil || m.picker.State() != conversationview.StateClosing || !m.conversationClosing {
		t.Fatalf("pending close state: mode=%v picker=%v state=%v closing=%v", m.mode, m.picker != nil, m.picker.State(), m.conversationClosing)
	}
	if m.editor.Value() != "draft survives" {
		t.Fatalf("pending close changed draft: %q", m.editor.Value())
	}

	msg := runResumeCmd(t, wait)
	_, _ = m.Update(msg)
	if m.mode != ModeChat || m.picker != nil || m.conversationClosing || engine.OperationActive() {
		t.Fatalf("drained close state: mode=%v picker=%v closing=%v active=%v", m.mode, m.picker != nil, m.conversationClosing, engine.OperationActive())
	}
	if m.editor.Value() != "draft survives" {
		t.Fatalf("drained close lost draft: %q", m.editor.Value())
	}
}

func TestQueuedResumeActionsCannotRestartLoadingOperation(t *testing.T) {
	t.Run("selection", func(t *testing.T) {
		backend := newResumeBackend()
		m := New(agent.New(backend, assistant.SendOptions{ConversationID: "old"}))
		summary := assistant.ConversationSummary{ConversationID: resumeConversationID, Title: "New"}
		backend.lists <- resumeListReply{response: summaries(summary)}
		listWait := m.openConversationPicker()
		_, _ = m.Update(runResumeCmd(t, listWait))

		first := m.selectConversation(summary)
		if first == nil || m.picker.State() != conversationview.StateLoading {
			t.Fatalf("first selection: cmd=%v state=%v", first != nil, m.picker.State())
		}
		if second := m.selectConversation(summary); second != nil {
			t.Fatal("queued second selection restarted the load")
		}

		_ = m.closeConversationPicker()
		_, _ = m.Update(runResumeCmd(t, first))
	})

	t.Run("retry", func(t *testing.T) {
		backend := newResumeBackend()
		m := New(agent.New(backend, assistant.SendOptions{ConversationID: "old"}))
		backend.lists <- resumeListReply{err: errors.New("unavailable")}
		listWait := m.openConversationPicker()
		_, _ = m.Update(runResumeCmd(t, listWait))

		first := m.retryConversationOperation()
		if first == nil || m.picker.State() != conversationview.StateLoading {
			t.Fatalf("first retry: cmd=%v state=%v", first != nil, m.picker.State())
		}
		if second := m.retryConversationOperation(); second != nil {
			t.Fatal("queued second retry restarted the load")
		}

		_ = m.closeConversationPicker()
		_, _ = m.Update(runResumeCmd(t, first))
	})
}

func TestResumeSwitchFailureRetryThenAtomicSuccess(t *testing.T) {
	backend := newResumeBackend()
	engine := agent.New(backend, assistant.SendOptions{ConversationID: "old"})
	m := New(engine)
	m.resize(50, 12)
	m.transcript.Blocks = []agent.Block{textBlock("same-message-id", "OLD-CACHED")}
	m.syncTranscript()
	_ = m.list.Render() // populate the old conversation's render cache

	summary := assistant.ConversationSummary{ConversationID: resumeConversationID, UpdatedAt: 10, Title: "New"}
	backend.lists <- resumeListReply{response: summaries(summary)}
	wait := m.openConversationPicker()
	_, _ = m.Update(runResumeCmd(t, wait))

	backend.histories <- resumeHistoryReply{err: errors.New("network down")}
	_, wait = m.Update(conversationview.SelectedMsg{Conversation: summary})
	_, _ = m.Update(runResumeCmd(t, wait))
	if m.picker.State() != conversationview.StateError || m.convID != "old" || engine.ConversationID() != "old" || m.transcript.Blocks[0].Markdown.Content != "OLD-CACHED" {
		t.Fatalf("failed load mutated state: picker=%v root=%q engine=%q blocks=%+v", m.picker.State(), m.convID, engine.ConversationID(), m.transcript.Blocks)
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
	if len(m.transcript.Blocks) != 1 || m.transcript.Blocks[0].Markdown.Content != "NEW-TRANSCRIPT" {
		t.Fatalf("new blocks = %+v", m.transcript.Blocks)
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
	// Escape wins the event race: the picker stays non-interactive until this
	// already-loaded candidate is discarded and engine ownership is released.
	_, _ = m.Update(conversationview.CancelledMsg{})
	if m.mode != ModeConversations || m.picker == nil || m.picker.State() != conversationview.StateClosing {
		t.Fatalf("cancel did not wait for loaded candidate drain: mode=%v picker=%v", m.mode, m.picker != nil)
	}
	_, _ = m.Update(loadedMsg)
	if m.mode != ModeChat || m.convID != "old" || engine.ConversationID() != "old" {
		t.Fatalf("stale loaded result switched state: mode=%v root=%q engine=%q", m.mode, m.convID, engine.ConversationID())
	}
}

// Esc while a client tool's input is half-streamed must leave a stopped
// block, not a frozen spinner: the settled state has to reach the TUI even
// though the turn's context is already cancelled.
func TestCancelledTurnSettlesStreamedClientToolInTUI(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	toolSet, err := agent.NewToolSet(agent.ModeSkipPermissions, tools.NewClientTools(ws)...)
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(&halfStreamedWriteBackend{}, assistant.SendOptions{}), Config{Tools: toolSet, Workspace: ws})
	m.resize(100, 30)

	setConversationInput(m, "write half.txt")
	_, _ = m.submit()
	// The backend waits right after its last delta, so once the TUI shows
	// the partial input, the turn is parked mid-input.
	for !hasPartialTool(m.transcript.Blocks, "write_file", "bravo") {
		if m.turnEvents == nil {
			t.Fatal("turn ended before the write_file input was half-streamed")
		}
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents)))
	}

	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainConversationRemote(t, m)

	tool := findToolBlock(m.transcript.Blocks, "write_file")
	if tool == nil || tool.Status != agent.ToolCancelled {
		t.Fatalf("write_file block = %+v, want cancelled and not running", tool)
	}
	if _, err := os.Stat(filepath.Join(ws.Path(), "half.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("half.txt stat error = %v, want not exist", err)
	}

	setConversationInput(m, "are you there?")
	_, _ = m.submit()
	drainConversationRemote(t, m)
	if m.chatPhase != chat.PhaseIdle || m.turnEvents != nil {
		t.Fatalf("follow-up turn did not complete: phase=%v active=%t", m.chatPhase, m.turnEvents != nil)
	}
	if !hasTextBlock(m.transcript.Blocks, "still alive") {
		t.Fatalf("follow-up transcript = %+v, want normal answer", m.transcript.Blocks)
	}
}

func hasPartialTool(blocks []agent.Block, name, fragment string) bool {
	tool := findToolBlock(blocks, name)
	return tool != nil && strings.Contains(tool.InputPartial, fragment)
}

func findToolBlock(blocks []agent.Block, name string) *agent.ToolBlock {
	for _, block := range blocks {
		if block.Tool != nil && block.Tool.Name == name {
			return block.Tool
		}
	}
	return nil
}

func hasTextBlock(blocks []agent.Block, text string) bool {
	for _, block := range blocks {
		if block.Kind == assistant.KindText && block.Markdown != nil && strings.Contains(block.Markdown.Content, text) {
			return true
		}
	}
	return false
}

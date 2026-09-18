package tui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

type cancellableConversationBackend struct {
	sendStarted    chan struct{}
	historyStarted chan struct{}
	cancelled      atomic.Int32
}

func newCancellableConversationBackend() *cancellableConversationBackend {
	return &cancellableConversationBackend{
		sendStarted:    make(chan struct{}, 1),
		historyStarted: make(chan struct{}, 1),
	}
}

func (b *cancellableConversationBackend) Send(ctx context.Context, _ any, _ assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.sendStarted <- struct{}{}
	<-ctx.Done()
	b.cancelled.Add(1)
	return "old", ctx.Err()
}

func (b *cancellableConversationBackend) ConversationHistory(ctx context.Context, _ assistant.ConversationHistoryInput) (*assistant.ConversationHistoryResponse, error) {
	b.historyStarted <- struct{}{}
	<-ctx.Done()
	b.cancelled.Add(1)
	return nil, ctx.Err()
}

type failedConversationBackend struct{ err error }

func (b *failedConversationBackend) Send(context.Context, any, assistant.SendOptions, func(assistant.AssistantResponse) error) (string, error) {
	return "old", b.err
}

type immediateConversationBackend struct{}

func (*immediateConversationBackend) Send(_ context.Context, _ any, opts assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	if opts.ConversationID == "" {
		return "fresh", nil
	}
	return opts.ConversationID, nil
}

type streamingConversationBackend struct{}

func (*streamingConversationBackend) Send(_ context.Context, _ any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	var response assistant.AssistantResponse
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("done"))
	return "conversation", emit(response)
}

func runConversationCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case msg := <-result:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("command timed out")
		return nil
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("backend operation did not start")
	}
}

func setConversationInput(m *Model, value string) {
	m.editor.Reset()
	_ = m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: value})
}

func drainConversationRemote(t *testing.T, m *Model) {
	t.Helper()
	for m.turnEvents != nil {
		msg := runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents))
		_, _ = m.Update(msg)
	}
}

func TestCompletedTurnAppliesFinalTranscriptSnapshot(t *testing.T) {
	m := New(agent.New(&streamingConversationBackend{}, assistant.SendOptions{}))
	setConversationInput(m, "prompt")
	_, _ = m.submit()
	drainConversationRemote(t, m)

	if m.chatPhase != chat.PhaseIdle {
		t.Fatalf("chat phase = %v, want idle", m.chatPhase)
	}
	if len(m.blocks) != 2 || !m.blocks[1].Complete {
		t.Fatalf("blocks = %+v, want completed assistant response", m.blocks)
	}
}

func TestNewAndClearResetConversationStateLocally(t *testing.T) {
	for _, input := range []string{"/new", "/clear", "/NEW", "/CLEAR"} {
		t.Run(input, func(t *testing.T) {
			m := newModelWithSpy(t)
			m.engine = agent.New(&spyBackend{t: t}, assistant.SendOptions{ConversationID: "old"})
			m.convID = "old"
			m.blocks = []agent.Block{{Markdown: &assistant.MarkdownPayload{Content: "old transcript"}}}
			m.usage = &assistant.Usage{TokensUsed: 99, MaxTokens: 100}
			m.chatPhase = chat.PhaseError
			m.notice = notice(chat.NoticeError, errors.New("old error"), "old error")
			setConversationInput(m, input)

			_, _ = m.submit()
			if !m.editor.Focused() {
				t.Fatal("reset left the editor unfocused")
			}
			if m.convID != "" || m.engine.ConversationID() != "" || len(m.blocks) != 0 {
				t.Fatalf("identity/transcript not reset: root=%q engine=%q blocks=%d", m.convID, m.engine.ConversationID(), len(m.blocks))
			}
			if m.usage != nil || m.chatPhase != chat.PhaseIdle || !m.notice.Empty() {
				t.Fatalf("turn state not reset: usage=%v phase=%v notice=%+v", m.usage, m.chatPhase, m.notice)
			}
		})
	}
}

func TestNewDuringActiveTurnCancelsOnceAndDrainsBeforeReset(t *testing.T) {
	backend := newCancellableConversationBackend()
	m := New(agent.New(backend, assistant.SendOptions{ConversationID: "old"}))
	setConversationInput(m, "active prompt")
	_, _ = m.submit()
	waitSignal(t, backend.sendStarted)

	originalCancel := m.cancelTurn
	var cancelCalls atomic.Int32
	m.cancelTurn = func() {
		cancelCalls.Add(1)
		originalCancel()
	}
	oldGeneration := m.turnGen
	setConversationInput(m, "/new")
	_, _ = m.submit()
	setConversationInput(m, "/clear")
	_, _ = m.submit()

	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel function calls = %d, want exactly 1", got)
	}
	if m.convID != "old" || !m.pendingNew {
		t.Fatalf("conversation reset before drain: id=%q pending=%v", m.convID, m.pendingNew)
	}
	drainConversationRemote(t, m)
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel function calls after drain = %d, want exactly 1", got)
	}
	if m.convID != "" || m.engine.ConversationID() != "" || m.turnGen == oldGeneration {
		t.Fatalf("post-drain reset failed: root=%q engine=%q generation=%d", m.convID, m.engine.ConversationID(), m.turnGen)
	}
}

func TestNewDuringHistoryLoadingCancelsAndDrains(t *testing.T) {
	backend := newCancellableConversationBackend()
	engine := agent.New(backend, assistant.SendOptions{ConversationID: "loading"})
	m := New(engine)
	ctx, cancel := context.WithCancel(context.Background())
	m.chatPhase = chat.PhaseLoading
	_ = m.beginRemote(engine.Restore(ctx), cancel)
	waitSignal(t, backend.historyStarted)

	setConversationInput(m, "/new")
	_, _ = m.submit()
	if m.convID != "loading" {
		t.Fatal("history-loading conversation reset before its operation drained")
	}
	drainConversationRemote(t, m)
	if m.convID != "" || engine.ConversationID() != "" || backend.cancelled.Load() != 1 {
		t.Fatalf("history reset result: root=%q engine=%q cancellations=%d", m.convID, engine.ConversationID(), backend.cancelled.Load())
	}
}

func TestNewAfterCancellationDoesNotCancelTwice(t *testing.T) {
	backend := newCancellableConversationBackend()
	m := New(agent.New(backend, assistant.SendOptions{ConversationID: "old"}))
	setConversationInput(m, "active prompt")
	_, _ = m.submit()
	waitSignal(t, backend.sendStarted)

	originalCancel := m.cancelTurn
	var cancelCalls atomic.Int32
	m.cancelTurn = func() {
		cancelCalls.Add(1)
		originalCancel()
	}
	_, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	setConversationInput(m, "/new")
	_, _ = m.submit()
	if got := cancelCalls.Load(); got != 1 {
		t.Fatalf("cancel function calls = %d after Esc then /new, want 1", got)
	}
	drainConversationRemote(t, m)
	if m.convID != "" || cancelCalls.Load() != 1 {
		t.Fatalf("post-cancellation reset: id=%q cancel calls=%d", m.convID, cancelCalls.Load())
	}
}

func TestNewAfterFailedTurnDrainsThenClearsError(t *testing.T) {
	m := New(agent.New(&failedConversationBackend{err: errors.New("turn failed")}, assistant.SendOptions{ConversationID: "old"}))
	setConversationInput(m, "failing prompt")
	_, _ = m.submit()

	for m.chatPhase != chat.PhaseError {
		msg := runConversationCmd(t, waitEvent(m.turnGen, m.turnEvents))
		_, _ = m.Update(msg)
	}
	setConversationInput(m, "/new")
	_, _ = m.submit()
	if m.convID != "old" || !m.pendingNew {
		t.Fatal("failed turn reset before its event channel drained")
	}
	drainConversationRemote(t, m)
	if m.convID != "" || m.chatPhase != chat.PhaseIdle || !m.notice.Empty() || m.usage != nil {
		t.Fatalf("failed-turn state survived reset: id=%q phase=%v notice=%+v usage=%v", m.convID, m.chatPhase, m.notice, m.usage)
	}
}

func TestNewRejectsStaleEventsFromPriorConversation(t *testing.T) {
	m := New(agent.New(&immediateConversationBackend{}, assistant.SendOptions{ConversationID: "old"}))
	setConversationInput(m, "/new")
	_, _ = m.submit()
	oldGeneration := m.turnGen - 1

	setConversationInput(m, "fresh prompt")
	_, _ = m.submit()
	freshGeneration := m.turnGen
	_, _ = m.Update(turnEventMsg{generation: oldGeneration, ev: agent.Event{Kind: agent.EventConversation, ConvID: "stale"}})
	_, _ = m.Update(turnClosedMsg{generation: oldGeneration})
	if m.convID != "" || m.turnGen != freshGeneration || m.turnEvents == nil {
		t.Fatalf("stale event mutated fresh operation: id=%q generation=%d active=%v", m.convID, m.turnGen, m.turnEvents != nil)
	}
	drainConversationRemote(t, m)
	if m.convID != "fresh" {
		t.Fatalf("first prompt after reset got conversation id %q, want fresh", m.convID)
	}
}

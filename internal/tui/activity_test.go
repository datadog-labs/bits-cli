package tui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

func TestWireReplayCompletionPublishesIdleTranscriptWithoutSubmitting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for _, line := range []string{
			`{"data":{"attributes":{"structured_message":{"role":"user","message_id":"web-user","content":{"type":"markdown_fragment","content":"new web prompt"}}}}}`,
			`{"data":{"attributes":{"structured_message":{"role":"assistant","message_id":"web-answer","content":{"type":"markdown_fragment","content":"new web answer"}}}}}`,
			`{"data":{"attributes":{"structured_message":{"role":"assistant","message_id":"replay_complete","content":{"type":"replay_complete","replay_kind":"full","cursor":null}}}}}`,
		} {
			if _, err := io.WriteString(w, line+"\n"); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	defer srv.Close()
	c := &assistant.Client{BaseURL: srv.URL, HTTPClient: srv.Client()}
	m := New(agent.New(c, agent.Config{ConversationID: "one"}))
	m.activityConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityIdle
	m.activitySnapshotReady = true
	m.activityReplay = agent.NewActivityReplay()
	err := c.SubscribeConversation(context.Background(), "one", func(a assistant.ConversationActivity) error {
		_, _ = m.Update(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: a}})
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if !m.activitySnapshotReady || m.activityNeedsSync || m.activityBlocksSubmission() {
		t.Fatalf("ready=%v sync=%v blocked=%v", m.activitySnapshotReady, m.activityNeedsSync, m.activityBlocksSubmission())
	}
	if len(m.state.Blocks) != 2 || m.state.Blocks[1].Markdown.Content != "new web answer" || len(m.engine.State().Blocks) != 2 {
		t.Fatalf("replayed transcript was not published: %+v", m.state.Blocks)
	}
	m.list.SetWidth(80)
	m.list.SetHeight(20)
	if got := ansi.Strip(m.list.Render()); strings.Contains(got, "[unknown]") || !strings.Contains(got, "new web answer") {
		t.Fatalf("replay completion rendered incorrectly: %q", got)
	}
}

func TestPromptRenderAfterSubscriptionSyncDoesNotReusePreviousPrompt(t *testing.T) {
	const conversationID = "conversation-1"
	backend := &recoveredUIBackend{messages: make(chan any, 4)}
	engine := agent.New(backend, agent.Config{ConversationID: conversationID})
	list := chat.NewList()
	list.SetStyles(chat.DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(100)
	runTestTurn(t, engine, "OLDER-CLI-PROMPT")
	list.SetItems(engine.State().Blocks)
	if got := list.Render(); !strings.Contains(got, "OLDER-CLI-PROMPT") {
		t.Fatalf("first prompt not rendered: %q", got)
	}
	user := assistant.AssistantMessage("persisted-user", assistant.TextContent("OLDER-CLI-PROMPT"))
	user.Role = "user"
	if _, err := engine.SyncConversationTranscript(conversationID, agent.NewActivityReplay([]assistant.Message{user}...), false); err != nil {
		t.Fatal(err)
	}
	runTestTurn(t, engine, "LATEST-CLI-PROMPT")
	list.SetItems(engine.State().Blocks)
	if got := list.Render(); !strings.Contains(got, "LATEST-CLI-PROMPT") {
		t.Fatalf("subscription sync reused a previous prompt's cached rendering: %q", got)
	}
}

type recoveredUIBackend struct {
	messages        chan any
	interruptedCall *assistant.Message
}

func (b *recoveredUIBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	var response assistant.AssistantResponse
	if b.interruptedCall != nil {
		response.Data.Attributes.StructuredMessage = *b.interruptedCall
		b.interruptedCall = nil
		if err := emit(response); err != nil {
			return "conversation-1", err
		}
		return "conversation-1", io.ErrUnexpectedEOF
	}
	b.messages <- message
	response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent("continued"))
	return "conversation-1", emit(response)
}

func TestRemoteActivityBlocksPromptAndPreservesDraft(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "conversation-1"
	m.activityState = activityRemoteTurn
	m.editor.Update(tea.PasteMsg{Content: "keep this draft"})

	_, cmd := m.submit()
	if cmd != nil {
		t.Fatal("blocked submission should behave like a busy local turn")
	}
	if got := m.editor.Value(); got != "keep this draft" {
		t.Fatalf("draft = %q, want preserved", got)
	}
	if m.op.run != 0 {
		t.Fatal("blocked submission started a local turn")
	}
}

func TestActivityMarkersAndReadySnapshotControlPromptGate(t *testing.T) {
	m := &Model{
		state:              agent.State{ConversationID: "conversation-1"},
		activityEvents:     make(chan agent.ActivityUpdate),
		activityGeneration: 4,
		activityState:      activityConnecting,
	}

	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 4, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady, Active: false},
	}})
	if m.activityBlocksSubmission() {
		t.Fatal("authoritative idle snapshot left submission blocked")
	}
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 4, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityStarted},
	}})
	if !m.activityBlocksSubmission() {
		t.Fatal("remote started marker did not block submission")
	}
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 4, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityEnded},
	}})
	if m.activityBlocksSubmission() {
		t.Fatal("ended marker did not restore submission")
	}
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 4, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady, Active: true},
	}})
	if !m.activityBlocksSubmission() {
		t.Fatal("mid-turn ready snapshot did not restore remote pending state")
	}
}

func TestReadyActivityClearsReconnectStateForQuietConversation(t *testing.T) {
	m := &Model{
		state:              agent.State{ConversationID: "conversation-1"},
		activityEvents:     make(chan agent.ActivityUpdate),
		activityGeneration: 4,
		activityState:      activityDisconnected,
		activityRetry:      3,
	}

	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 4, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady},
	}})
	if m.activityState != activityIdle || m.activityBlocksSubmission() {
		t.Fatalf("connected state = %v blocked=%v, want idle and enabled", m.activityState, m.activityBlocksSubmission())
	}
	if got := m.promptPlaceholder(); got != "Ask Bits…" {
		t.Fatalf("placeholder = %q, want quiet connected state", got)
	}
}

func TestReconnectClearsStaleActiveTurn(t *testing.T) {
	m := New(agent.New(&silentActivityBackend{spyBackend: &spyBackend{t: t}}, agent.Config{ConversationID: "conversation-1"}))
	m.state.ConversationID = "conversation-1"
	m.activityRetry = 3
	m.activityState = activityDisconnected
	m.activityTurnActive = true

	_ = m.connectConversationSubscription("conversation-1")
	defer m.stopConversationSubscription()
	if m.activityTurnActive || m.activityState != activityDisconnected {
		t.Fatalf("reconnect active=%v state=%v, want cleared disconnected attempt", m.activityTurnActive, m.activityState)
	}
}

func TestReconnectPreservesLocallyOwnedActiveTurn(t *testing.T) {
	m := New(agent.New(&silentActivityBackend{spyBackend: &spyBackend{t: t}}, agent.Config{ConversationID: "conversation-1"}))
	m.state.ConversationID = "conversation-1"
	m.activityRetry = 3
	m.activityState = activityDisconnected
	m.activityTurnActive = true
	m.localTurnOwned = true
	m.localTurnRecoverable = true

	_ = m.connectConversationSubscription("conversation-1")
	defer m.stopConversationSubscription()
	if !m.activityTurnActive || !m.activityBlocksSubmission() {
		t.Fatalf("local reconnect active=%v blocked=%v, want preserved and blocked", m.activityTurnActive, m.activityBlocksSubmission())
	}
}

func TestReadyActivityEnablesResumedIdleConversation(t *testing.T) {
	m := &Model{
		state:              agent.State{ConversationID: "conversation-1"},
		activityEvents:     make(chan agent.ActivityUpdate),
		activityGeneration: 4,
		activityState:      activityConnecting,
	}

	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 4, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady},
	}})
	if m.activityState != activityIdle || m.activityBlocksSubmission() {
		t.Fatalf("resumed connected state = %v blocked=%v, want idle and enabled", m.activityState, m.activityBlocksSubmission())
	}
	if got := m.promptPlaceholder(); got != "Ask Bits…" {
		t.Fatalf("placeholder = %q, want no visible checking state", got)
	}
}

func TestLocalTurnDoesNotBecomeRemoteFromSubscriptionReplay(t *testing.T) {
	m := &Model{
		state:              agent.State{ConversationID: "conversation-1"},
		op:                 operation{kind: opTurn, run: 1},
		activityEvents:     make(chan agent.ActivityUpdate),
		activityGeneration: 3,
		activityState:      activityConnecting,
	}
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 3, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityStarted},
	}})
	if m.activityState == activityRemoteTurn {
		t.Fatal("local turn's replayed started marker was classified as remote")
	}
	m.localTurnOwned = true
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 3, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady, Active: false},
	}})
	if !m.localTurnOwned {
		t.Fatal("idle snapshot racing a local request discarded turn ownership")
	}
}

func TestStaleActivityEventCannotChangeCurrentConversation(t *testing.T) {
	m := &Model{
		state:              agent.State{ConversationID: "new-conversation"},
		activityEvents:     make(chan agent.ActivityUpdate),
		activityGeneration: 9,
		activityState:      activityIdle,
	}
	cmd := m.applyActivityUpdate(activityUpdateMsg{generation: 8, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityStarted},
	}})
	if cmd != nil || m.activityState != activityIdle {
		t.Fatalf("stale event changed state: cmd=%v state=%v", cmd != nil, m.activityState)
	}
}

func TestDisconnectedRemoteTurnBlocksAndSchedulesRetry(t *testing.T) {
	m := &Model{
		state:                  agent.State{ConversationID: "conversation-1"},
		activityConversationID: "conversation-1",
		activityEvents:         make(chan agent.ActivityUpdate),
		activityGeneration:     2,
		activityTurnActive:     true,
		activityState:          activityRemoteTurn,
	}
	cmd := m.handleActivityDisconnect(2, errors.New("dropped"))
	if cmd == nil || m.activityState != activityDisconnected || !m.activityBlocksSubmission() {
		t.Fatalf("disconnect state = %v blocked=%v cmd=%v", m.activityState, m.activityBlocksSubmission(), cmd != nil)
	}
	if got := m.promptPlaceholder(); got != "Reconnecting…" {
		t.Fatalf("placeholder = %q, want reconnecting state", got)
	}
}

func TestReadyReplayResumesPendingToolForLocallyOwnedTurn(t *testing.T) {
	backend := &recoveredUIBackend{messages: make(chan any, 1)}
	engine := agent.New(backend, agent.Config{ConversationID: "conversation-1"})
	runs := 0
	tools, err := agent.NewToolSet(agent.Tool{
		Definition: assistant.ClientTool{Name: "calculate"},
		Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
			runs++
			return agent.ToolResult{Output: "42"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(engine, Config{Tools: tools})
	m.activityConversationID = "conversation-1"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityConnecting
	m.activityReplay = agent.NewActivityReplay()
	m.localTurnOwned = true
	m.localTurnRecoverable = true
	m.localTurnInput = agent.TurnInput{Tools: tools}

	call := assistant.ToolCallContent("call-1", "calculate", `{}`)
	call.Type = assistant.ContentClientToolCall
	observed := assistant.AssistantMessage("tool-call", call)
	backend.interruptedCall = &observed
	events, stop := engine.Subscribe()
	defer stop()
	run, err := engine.StartTurn(context.Background(), agent.TurnInput{Message: "calculate", Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	for event := range agent.Follow(events, run) {
		if event.Terminal() && event.Kind != agent.EventError {
			t.Fatalf("expected interrupted local turn: %+v", event)
		}
	}
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{
			Kind:    assistant.ConversationActivityMessage,
			Message: assistant.AssistantMessage("tool-call", call),
		},
	}})
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady, Active: true},
	}})
	if m.op.run == 0 || m.chatPhase != chat.PhaseWaiting {
		t.Fatalf("recovery did not start: events=%v phase=%v", m.op.run != 0, m.chatPhase)
	}
	drainConversationRemote(t, m)
	message := <-backend.messages
	responses, ok := message.([]assistant.ClientToolResponse)
	if !ok || len(responses) != 1 || responses[0].ToolCallID != "call-1" || runs != 1 {
		t.Fatalf("message=%T %+v runs=%d", message, message, runs)
	}
}

func TestReadyReplayRendersContinuingLocallyOwnedTurn(t *testing.T) {
	const conversationID = "conversation-1"
	m := New(agent.New(&spyBackend{t: t}, agent.Config{ConversationID: conversationID}))
	m.activityConversationID = conversationID
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityDisconnected
	m.activityReplay = agent.NewActivityReplay()
	m.localTurnOwned = true
	m.localTurnRecoverable = true

	message := assistant.AssistantMessage("answer", assistant.TextContent("continued after reconnect"))
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityMessage, Message: message},
	}})
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady, Active: true},
	}})
	if m.activityState != activityRemoteTurn || len(m.state.Blocks) != 1 ||
		m.state.Blocks[0].Markdown == nil || m.state.Blocks[0].Markdown.Content != "continued after reconnect" {
		t.Fatalf("reconnected turn state=%v blocks=%+v", m.activityState, m.state.Blocks)
	}
	if !m.activityBlocksSubmission() || m.promptPlaceholder() != "Working on it…" {
		t.Fatalf("continuing turn blocked=%v placeholder=%q", m.activityBlocksSubmission(), m.promptPlaceholder())
	}

	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityEnded},
	}})
	if m.activityBlocksSubmission() || m.localTurnOwned {
		t.Fatalf("ended turn blocked=%v locallyOwned=%v", m.activityBlocksSubmission(), m.localTurnOwned)
	}
	if got := m.engine.State().Blocks; len(got) != 1 || got[0].Markdown == nil || got[0].Markdown.Content != "continued after reconnect" {
		t.Fatalf("engine transcript = %+v", got)
	}
}

func TestRemoteSubscriptionUpdatesTranscriptWithoutCreatingApproval(t *testing.T) {
	const conversationID = "conversation-1"
	m := New(agent.New(&spyBackend{t: t}, agent.Config{ConversationID: conversationID}))
	m.activityConversationID = conversationID
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityIdle
	m.activitySnapshotReady = true
	m.activityReplay = agent.NewActivityReplay()

	user := assistant.AssistantMessage("web-user", assistant.TextContent("from web"))
	user.Role = "user"
	tool := assistant.ToolCallContent("web-tool", "web_only_tool", `{}`)
	tool.Type = assistant.ContentClientToolCall
	messages := []assistant.Message{
		user,
		assistant.AssistantMessage("web-answer", assistant.TextContent("remote answer")),
		assistant.AssistantMessage("web-tool-message", tool),
	}

	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityStarted},
	}})
	for _, message := range messages {
		_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
			Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityMessage, Message: message},
		}})
	}
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{
		Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityEnded},
	}})

	if len(m.state.Blocks) != 3 || m.state.Blocks[1].Markdown == nil || m.state.Blocks[1].Markdown.Content != "remote answer" {
		t.Fatalf("remote transcript = %+v", m.state.Blocks)
	}
	if len(m.approvals) != 0 {
		t.Fatalf("passive client tool created approvals: %+v", m.approvals)
	}
	if got := m.engine.State().Blocks; len(got) != 3 || got[1].Markdown == nil || got[1].Markdown.Content != "remote answer" {
		t.Fatalf("engine transcript was not synchronized: %+v", got)
	}
}

func runTestTurn(t *testing.T, e *agent.Engine, text string) {
	t.Helper()
	events, stop := e.Subscribe()
	defer stop()
	run, err := e.StartTurn(context.Background(), agent.TurnInput{Message: text})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range agent.Follow(events, run) {
		if ev.Kind == agent.EventError {
			t.Fatal(ev.Err)
		}
	}
}

func TestRemoteActivityStartsAndStopsLoadingAnimation(t *testing.T) {
	m := newModelWithSpy(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.state.ConversationID = "conversation-1"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityIdle
	m.activitySnapshotReady = true
	_, _ = m.Update(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityStarted}}})
	if !m.activityBlocksSubmission() || !m.animBorderSweep.active {
		t.Fatal("remote turn did not disable submission and animate")
	}
	_, _ = m.Update(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityEnded}}})
	if m.activityBlocksSubmission() || m.animBorderSweep.active {
		t.Fatal("ended remote turn left submission disabled or animation running")
	}
}

func TestBackendFailureRequiresFreshReplayBeforeRecovery(t *testing.T) {
	m := New(agent.New(&silentActivityBackend{spyBackend: &spyBackend{t: t}}, agent.Config{ConversationID: "one"}))
	t.Cleanup(m.engine.Close)
	m.state.ConversationID = "conversation-1"
	m.op = operation{kind: opTurn, run: 1, cancel: func() {}}
	m.localTurnOwned = true
	m.activitySnapshotReady = true
	m.activityTurnActive = true
	m.activityGeneration = 4
	_ = m.applyRunEvent(agent.Event{Kind: agent.EventError, Run: 1, BackendFailure: true, Err: errors.New("disconnected")})
	defer m.stopConversationSubscription()
	if m.activitySnapshotReady || m.activityGeneration <= 4 || !m.localTurnRecoverable {
		t.Fatal("recovery did not request a fresh replay")
	}
}

func TestForegroundReplayRefreshesCachedAnswerWithSameMessageID(t *testing.T) {
	engine := agent.New(&spyBackend{t: t}, agent.Config{ConversationID: "one"})
	defer engine.Close()
	list := chat.NewList()
	list.SetStyles(chat.DefaultStyles(true))
	list.SetWidth(80)
	list.SetHeight(20)
	for _, text := range []string{"first answer", "latest answer"} {
		state, err := engine.SyncConversationTranscript("one", agent.NewActivityReplay([]assistant.Message{assistant.AssistantMessage("same-id", assistant.TextContent(text))}...), false)
		if err != nil {
			t.Fatal(err)
		}
		list.SetItems(state.Blocks)
		if got := ansi.Strip(list.Render()); !strings.Contains(got, text) {
			t.Fatalf("cached answer: %q, want %q", got, text)
		}
	}
}

func TestHistoricalEndMarkerDoesNotAuthorizeToolRecoveryBeforeReplayComplete(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityConnecting
	m.localTurnOwned, m.localTurnRecoverable = true, true
	for _, kind := range []assistant.ConversationActivityKind{assistant.ConversationActivityEnded, assistant.ConversationActivityStarted} {
		_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: assistant.ConversationActivity{Kind: kind}}})
	}
	if m.activitySnapshotReady || !m.localTurnOwned {
		t.Fatal("historical end marker changed local ownership before replay completed")
	}
}

func TestReplayMarkersDoNotBlockSubmissionBeforeReady(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityConnecting
	m.activityReplay = agent.NewActivityReplay()
	for _, kind := range []assistant.ConversationActivityKind{assistant.ConversationActivityStarted, assistant.ConversationActivityEnded} {
		_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: assistant.ConversationActivity{Kind: kind}}})
		if m.activityBlocksSubmission() {
			t.Fatal("historical marker blocked submission before replay completed")
		}
	}
	if strings.Contains(m.promptPlaceholder(), "Checking") {
		t.Fatal("initial subscription displayed a checking state")
	}
}

func TestHealthySubscriptionExpiryReconnectsQuietly(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityIdle
	m.activitySnapshotReady = true
	cmd := m.handleActivityDisconnect(1, io.EOF)
	if cmd == nil || m.activityState != activityConnecting || m.promptPlaceholder() == "Reconnecting…" {
		t.Fatal("normal idle expiry was displayed as a connection failure")
	}
	if m.activityBlocksSubmission() {
		t.Fatal("idle reconnect blocked submission")
	}
}

func TestActivityEndDoesNotClearOwnershipOfOpenLocalRequest(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activitySnapshotReady = true
	m.localTurnOwned = true
	m.op = operation{kind: opTurn, run: 1}
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityEnded}}})
	if !m.localTurnOwned {
		t.Fatal("subscription ended marker cleared ownership before the local request finished")
	}
}

type silentActivityBackend struct{ *spyBackend }

func (*silentActivityBackend) SubscribeConversation(ctx context.Context, _ string, _ func(assistant.ConversationActivity) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRefusedActivityDoesNotReconnectForever(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityConnecting
	m.activityReplay = agent.NewActivityReplay(assistant.AssistantMessage("partial", assistant.TextContent("incomplete replay")))
	m.activityNeedsSync = true
	m.handleActivityDisconnect(1, &assistant.APIError{StatusCode: http.StatusForbidden})
	if m.activityState != activityUnavailable || m.activityBlocksSubmission() {
		t.Fatal("refused subscription kept submission blocked")
	}
	if m.activityNeedsSync || m.activityReplay != nil {
		t.Fatal("refused subscription retained an incomplete replay for the next prompt")
	}
	if len(m.notices) != 1 {
		t.Fatal("refused subscription did not explain why live observation stopped")
	}
}

func TestAttachedSubscriptionDropsWithoutCleanEOFReconnectQuietly(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityIdle
	m.activitySnapshotReady = true
	m.activityRetry = 3
	if cmd := m.handleActivityDisconnect(1, io.ErrUnexpectedEOF); cmd == nil {
		t.Fatal("dropped attachment was not retried")
	}
	if m.activityState != activityConnecting || m.activityRetry != 1 {
		t.Fatalf("state=%v retry=%d", m.activityState, m.activityRetry)
	}
}

func TestUnrecoverableActivityStatusesStopReconnecting(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusMethodNotAllowed, http.StatusNotImplemented} {
		m := newModelWithSpy(t)
		m.state.ConversationID = "one"
		m.activityEvents = make(chan agent.ActivityUpdate)
		m.activityGeneration = 1
		m.activityState = activityConnecting
		m.handleActivityDisconnect(1, &assistant.APIError{StatusCode: code})
		if m.activityState != activityUnavailable || len(m.notices) != 1 {
			t.Fatalf("status %d: state=%v", code, m.activityState)
		}
	}
}

func TestCompletedTurnKeepsHealthySubscription(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityConversationID = "one"
	events := make(chan agent.ActivityUpdate)
	m.activityEvents = events
	m.activityGeneration = 1
	m.activityState = activityIdle
	m.activitySnapshotReady = true
	m.op = operation{kind: opTurn, run: 1}
	m.endOperation()
	if m.activityEvents != events || m.activityGeneration != 1 {
		t.Fatal("a healthy subscription was restarted after a completed turn")
	}
}

func TestCompletedTurnReattachesUntilSubscriptionCatchesUp(t *testing.T) {
	ready := assistant.ConversationActivity{Kind: assistant.ConversationActivityReady}
	activeReady := assistant.ConversationActivity{Kind: assistant.ConversationActivityReady, Active: true}
	started := assistant.ConversationActivity{Kind: assistant.ConversationActivityStarted}
	ended := assistant.ConversationActivity{Kind: assistant.ConversationActivityEnded}
	for _, tc := range []struct {
		name     string
		attached bool // replay already complete when the turn was sent
		markers  []assistant.ConversationActivity
		keep     bool
	}{
		// Only the user echo has arrived when the local request completes.
		{name: "no live end", attached: true},
		{name: "live start and end", attached: true, markers: []assistant.ConversationActivity{started, ended}, keep: true},
		{name: "end without live start", attached: true, markers: []assistant.ConversationActivity{ended}},
		{name: "active replay then end", markers: []assistant.ConversationActivity{activeReady, ended}, keep: true},
		{name: "historical markers", markers: []assistant.ConversationActivity{started, ended, ready}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModelWithSpy(t)
			m.state.ConversationID = "one"
			m.activityConversationID = "one"
			m.activityEvents = make(chan agent.ActivityUpdate)
			m.activityGeneration = 1
			m.activityState = activityConnecting
			m.activitySnapshotReady = tc.attached
			m.activityReplay = agent.NewActivityReplay()
			m.activityLocalTurn = localTurnSent
			m.op = operation{kind: opTurn, run: 1}
			for _, a := range tc.markers {
				_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: a}})
			}
			m.op.run = 0
			m.endOperation()
			if restarted := m.activityGeneration != 1; restarted == tc.keep {
				t.Fatalf("keep=%v restarted=%v", tc.keep, restarted)
			}
		})
	}
}

func TestSubscriptionRestartKeepsPendingLocalTurn(t *testing.T) {
	m := newModelWithSpy(t)
	m.activityLocalTurn = localTurnSent
	// A new conversation attaches when the local response reports its ID.
	m.stopConversationSubscription()
	if m.activityLocalTurn != localTurnSent {
		t.Fatal("attaching a subscription forgot the pending local turn")
	}
}

func TestUnfinishedReplayDoesNotReplaceTranscript(t *testing.T) {
	engine := agent.New(&recoveredUIBackend{messages: make(chan any, 4)}, agent.Config{ConversationID: "one"})
	t.Cleanup(engine.Close)
	first := assistant.AssistantMessage("first", assistant.TextContent("first answer"))
	second := assistant.AssistantMessage("second", assistant.TextContent("second answer"))
	if _, err := engine.SyncConversationTranscript("one", agent.NewActivityReplay(first, second), false); err != nil {
		t.Fatal(err)
	}
	m := New(engine)
	m.state = engine.State()
	m.activityConversationID = "one"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityGeneration = 1
	m.activityState = activityConnecting
	m.activityReplay = agent.NewActivityReplay(first)
	m.activityNeedsSync = true
	if !m.syncActivityTranscript() {
		t.Fatal("unfinished replay refused the prompt")
	}
	if got := len(engine.State().Blocks); got != 2 || !m.activityNeedsSync {
		t.Fatalf("unfinished replay was applied: blocks=%d", got)
	}
}

func TestCtrlDDoesNotQuitDuringRemoteTurn(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityState = activityRemoteTurn
	_, cmd := m.handleEditorKey(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("ctrl+d quit during a remote foreground turn")
	}
}

func TestActivityReconnectFailuresDoNotHoldPrompts(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityConversationID = "one"
	m.activityGeneration = 1
	for range 5 {
		m.activityEvents = make(chan agent.ActivityUpdate)
		if cmd := m.handleActivityDisconnect(1, errors.New("dial failed")); cmd == nil {
			t.Fatal("missing retry")
		}
		if m.activityBlocksSubmission() || len(m.notices) != 0 {
			t.Fatal("reconnect failure blocked submission or posted a notice")
		}
	}
}

func TestActivityNotFoundRetriesQuietly(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityConversationID = "one"
	m.activityGeneration = 1
	for range activityNotFoundRetries - 1 {
		m.activityEvents = make(chan agent.ActivityUpdate)
		if cmd := m.handleActivityDisconnect(1, &assistant.APIError{StatusCode: http.StatusNotFound}); cmd == nil {
			t.Fatal("404 did not schedule a retry")
		}
		if m.activityState != activityConnecting || m.activityBlocksSubmission() || len(m.notices) != 0 {
			t.Fatalf("404 state=%v blocked=%v notices=%d", m.activityState, m.activityBlocksSubmission(), len(m.notices))
		}
	}
	m.activityEvents = make(chan agent.ActivityUpdate)
	_ = m.applyActivityUpdate(activityUpdateMsg{generation: 1, update: agent.ActivityUpdate{Activity: assistant.ConversationActivity{Kind: assistant.ConversationActivityReady}}})
	if m.activityState != activityIdle || m.activityNotFound != 0 {
		t.Fatal("subscription did not recover after 404")
	}
}

func TestPersistentActivityNotFoundStopsRetrying(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityConversationID = "one"
	m.activityGeneration = 1
	var cmd tea.Cmd
	for range activityNotFoundRetries {
		m.activityEvents = make(chan agent.ActivityUpdate)
		cmd = m.handleActivityDisconnect(1, &assistant.APIError{StatusCode: http.StatusNotFound})
	}
	if cmd != nil || m.activityState != activityUnavailable {
		t.Fatalf("persistent 404 kept retrying: state=%v", m.activityState)
	}
	if len(m.notices) != 1 || m.notices[0].Notice.Text != "Live updates are unavailable for this conversation." {
		t.Fatalf("notices = %+v, want one unavailable notice", m.notices)
	}
	if m.activityBlocksSubmission() {
		t.Fatal("unavailable subscription blocked submission")
	}
}

func TestStaleTurnMarkerStopsHoldingPromptsWhenReconnectsFail(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityConversationID = "one"
	m.activityGeneration = 1
	m.activityTurnActive = true
	// A failed local turn keeps ownership, so reconnects keep the marker.
	m.localTurnOwned = true
	m.localTurnRecoverable = true
	for i := range activityStaleTurnRetries {
		m.activityEvents = make(chan agent.ActivityUpdate)
		if cmd := m.handleActivityDisconnect(1, errors.New("dial failed")); cmd == nil {
			t.Fatal("missing retry")
		}
		if blocked := m.activityBlocksSubmission(); blocked != (i < activityStaleTurnRetries-1) {
			t.Fatalf("after %d failures blocked=%v", i+1, blocked)
		}
	}
	if !m.localTurnOwned {
		t.Fatal("failing open dropped ownership of the recoverable turn")
	}
}

func TestLocalTurnCompletionReleasesActivityGate(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityTurnActive = true
	m.localTurnOwned = true
	_ = m.applyEvent(agent.Event{Kind: agent.EventDone})
	if m.activityBlocksSubmission() {
		t.Fatal("own finished turn held the next prompt before its ended marker")
	}
}

func TestBlockedSubmissionExplainsWhy(t *testing.T) {
	m := newModelWithSpy(t)
	m.state.ConversationID = "one"
	m.activityState = activityRemoteTurn
	m.editor.SetValue("hello")
	m.submit()
	if len(m.notices) != 1 || m.editor.Value() != "hello" {
		t.Fatalf("notices=%d draft=%q", len(m.notices), m.editor.Value())
	}
}

func TestSubmitDuringReplayKeepsExistingTranscript(t *testing.T) {
	backend := &recoveredUIBackend{messages: make(chan any, 4)}
	engine := agent.New(backend, agent.Config{ConversationID: "conversation-1"})
	t.Cleanup(engine.Close)
	first := assistant.AssistantMessage("first", assistant.TextContent("first answer"))
	second := assistant.AssistantMessage("second", assistant.TextContent("second answer"))
	if _, err := engine.SyncConversationTranscript("conversation-1", agent.NewActivityReplay(first, second), false); err != nil {
		t.Fatal(err)
	}
	m := New(engine)
	m.activityConversationID = "conversation-1"
	m.activityEvents = make(chan agent.ActivityUpdate)
	m.activityState = activityConnecting
	m.activityReplay = agent.NewActivityReplay(first)
	m.activityNeedsSync = true
	m.editor.SetValue("next prompt")
	m.submit()
	if m.op.run == 0 || m.editor.Value() != "" || len(m.notices) != 0 {
		t.Fatal("submission was held during replay")
	}
	for m.op.busy() {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	if blocks := engine.State().Blocks; len(blocks) != 4 || blocks[1].Markdown.Content != "second answer" || blocks[2].Markdown.Content != "next prompt" {
		t.Fatalf("submission lost existing transcript: %+v", blocks)
	}
}

package tui

import (
	"errors"
	"image/color"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// testModel returns a sized model with a no-op engine (the backend is never
// called because these tests drive Update with events directly).
func testModel(t *testing.T) *Model {
	t.Helper()
	m := New(agent.New(nil, assistant.SendOptions{}))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.mode != ModeChat {
		t.Fatal("model not in chat mode after WindowSizeMsg")
	}
	return m
}

func (m *Model) feed(ev agent.Event) { m.Update(turnEventMsg{ev: ev}) }

func (m *Model) feedMsg(msg assistant.Message) {
	m.feed(agent.Event{Kind: agent.EventMessage, Msg: msg})
}

func TestModel_StreamsDeltasIntoView(t *testing.T) {
	m := testModel(t)
	m.feedMsg(assistant.AssistantMessage("m1", assistant.TextContent("hello ")))
	m.feedMsg(assistant.AssistantMessage("m1", assistant.TextContent("world")))

	// Assistant text renders as markdown, so strip ANSI before matching: glamour
	// colors each word separately, splitting the literal substring in raw output.
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "hello world") {
		t.Errorf("view missing concatenated stream:\n%s", got)
	}
	if m.chatPhase != chat.PhaseStreaming {
		t.Errorf("chatPhase = %v, want streaming", m.chatPhase)
	}
}

// feedLines appends n distinct one-line assistant messages, each its own item.
func (m *Model) feedLines(n int) {
	for i := range n {
		id := "m" + strconv.Itoa(i)
		m.feedMsg(assistant.AssistantMessage(id, assistant.TextContent("line"+strconv.Itoa(i))))
	}
}

// The view auto-follows the tail as content streams in past the viewport. This
// is the regression guard for the in-place-mutation bug: the transcript grows
// its items in place, so deriving "was at bottom" from post-mutation state fails
// — the list must track following explicitly.
func TestModel_SticksToTailAsContentGrows(t *testing.T) {
	m := testModel(t)
	m.feedLines(40) // far more than the ~22-row transcript region

	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "line39") {
		t.Errorf("tail not visible; latest line missing:\n%s", got)
	}
	if strings.Contains(got, "line0") {
		t.Errorf("expected the top to have scrolled off while following:\n%s", got)
	}
}

// Scrolling up releases the tail pin: new content no longer yanks the view to
// the bottom. Scrolling back to the bottom resumes following.
func TestModel_ScrollUpReleasesTailThenResumes(t *testing.T) {
	m := testModel(t)
	m.feedLines(40)

	m.list.PageUp()
	if m.list.Following() {
		t.Fatal("PageUp over overflowing content should stop following")
	}
	m.feedMsg(assistant.AssistantMessage("later", assistant.TextContent("brandnew")))
	if got := ansi.Strip(m.View().Content); strings.Contains(got, "brandnew") {
		t.Errorf("released view should not jump to new tail content:\n%s", got)
	}

	m.list.ScrollToBottom()
	if got := ansi.Strip(m.View().Content); !strings.Contains(got, "brandnew") {
		t.Errorf("scrolling back to bottom should reveal the tail:\n%s", got)
	}
}

// Submitting a prompt always jumps to the tail and re-engages auto-follow, even
// if the user had scrolled up to read earlier history.
func TestModel_SubmitJumpsToTail(t *testing.T) {
	m := New(agent.New(fake.New(), assistant.SendOptions{}))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.Focus() // the textarea ignores input while blurred

	m.feedLines(40) // overflow the transcript
	_ = m.View()    // render pins to the tail (following)
	m.list.PageUp() // scroll up: release the tail pin
	if m.list.Following() {
		t.Fatal("precondition: PageUp should release follow")
	}

	m.Update(tea.PasteMsg{Content: "what changed?"})
	_, cmd := m.submit()
	if m.cancelTurn != nil {
		defer m.cancelTurn() // stop the fake turn's goroutine
	}
	if cmd == nil {
		t.Fatal("submit should start a turn")
	}

	if !m.list.Following() || !m.list.AtBottom() {
		t.Errorf("submit must jump to the tail and follow (following=%v, atBottom=%v)",
			m.list.Following(), m.list.AtBottom())
	}
}

func TestModel_TurnDoneReturnsToIdle(t *testing.T) {
	m := testModel(t)
	m.feedMsg(assistant.AssistantMessage("m1", assistant.TextContent("hi")))
	m.feed(agent.Event{Kind: agent.EventTurnDone})

	if m.chatPhase != chat.PhaseIdle {
		t.Errorf("chatPhase = %v, want idle after turn done", m.chatPhase)
	}
	if !m.notice.Empty() {
		t.Errorf("a clean turn should leave no notice; got %+v", m.notice)
	}
	if got := m.View().Content; !strings.Contains(got, "hi") {
		t.Errorf("view missing message:\n%s", got)
	}
}

func TestModel_BackgroundColorMsgReStylesTranscript(t *testing.T) {
	m := testModel(t)
	if m.chatStyles.MarkdownStyle != "dark" {
		t.Fatalf("default markdown style = %q, want dark", m.chatStyles.MarkdownStyle)
	}
	m.feedMsg(assistant.AssistantMessage("m1", assistant.TextContent("hello world")))
	dark := m.View().Content

	// A light (white) background must flip styles and re-render the transcript.
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	light := m.View().Content

	if m.hasDarkBG {
		t.Error("hasDarkBG should be false after a light background message")
	}
	if m.chatStyles.MarkdownStyle != "light" {
		t.Errorf("markdown style = %q, want light", m.chatStyles.MarkdownStyle)
	}
	if dark == light {
		t.Error("expected transcript to re-style when the background changes")
	}
}

func TestModel_ToolAndErrorRender(t *testing.T) {
	m := testModel(t)
	m.feedMsg(assistant.AssistantMessage("m1",
		assistant.ToolResultContent("tc1", "run_bash", assistant.ToolStatusSuccess, "ok")))
	m.feed(agent.Event{Kind: agent.EventError, Err: errors.New("boom")})

	got := m.View().Content
	for _, want := range []string{"run_bash", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q:\n%s", want, got)
		}
	}
	if m.notice.Level != chat.NoticeError {
		t.Errorf("notice level = %v, want error", m.notice.Level)
	}
}

// A call and its result arrive on separate messages but must fold into one block.
func TestModel_ToolCallAndResultMergeIntoOneItem(t *testing.T) {
	m := testModel(t)
	m.feedMsg(assistant.AssistantMessage("m1", assistant.ToolCallContent("tc1", "run_bash", `{"cmd":"ls"}`)))
	m.feedMsg(assistant.AssistantMessage("m2",
		assistant.ToolResultContent("tc1", "", assistant.ToolStatusSuccess, "ok")))

	items := m.transcript.Items()
	if len(items) != 1 {
		t.Fatalf("want 1 tool item, got %d", len(items))
	}
	if items[0].Tool.Output != "ok" || items[0].Tool.Status != chat.ToolSuccess {
		t.Errorf("result did not merge: %+v", items[0].Tool)
	}
}

// One message that reasons and then answers renders as two blocks.
func TestModel_ReasoningAndTextAreSeparateItems(t *testing.T) {
	m := testModel(t)
	m.feedMsg(assistant.AssistantMessage("m1", assistant.ThinkingContent("let me think")))
	m.feedMsg(assistant.AssistantMessage("m1", assistant.TextContent("the answer")))

	items := m.transcript.Items()
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Kind != assistant.KindReasoning || items[1].Kind != assistant.KindText {
		t.Errorf("kinds = %v, %v; want reasoning then text", items[0].Kind, items[1].Kind)
	}
}

// The usage final fragment is empty: bank the usage, render no block.
func TestModel_UsageFromMessageResults(t *testing.T) {
	m := testModel(t)
	msg := assistant.AssistantMessage("m1", assistant.TextContent(""))
	msg.Results = &assistant.Results{Usage: &assistant.Usage{TokensUsed: 42, MaxTokens: 200000}}
	m.feedMsg(msg)

	if m.usage == nil || m.usage.TokensUsed != 42 {
		t.Fatalf("usage = %+v, want TokensUsed 42", m.usage)
	}
	if got := len(m.transcript.Items()); got != 0 {
		t.Errorf("want no items for an empty fragment, got %d", got)
	}
}

// A restored conversation replays through the same folding as the live stream:
// user and assistant text and a tool block all appear, and the model lands
// idle-ready with no streaming indicators.
func TestModel_RestoreConversationReplaysHistory(t *testing.T) {
	m := testModel(t)
	m.Update(historyLoadedMsg{msgs: []assistant.Message{
		{Role: "user", MessageID: "u1", Content: assistant.TextContent("why is latency high")},
		assistant.AssistantMessage("a1", assistant.ToolCallContent("tc1", "search_logs", `{"q":"errors"}`)),
		assistant.AssistantMessage("a2", assistant.ToolResultContent("tc1", "", assistant.ToolStatusSuccess, "3 hits")),
		assistant.AssistantMessage("a3", assistant.TextContent("a deploy regressed the p99")),
	}})

	got := m.View().Content
	for _, want := range []string{"why is latency high", "search_logs", "a deploy regressed the p99"} {
		if !strings.Contains(got, want) {
			t.Errorf("restored view missing %q:\n%s", want, got)
		}
	}
	if m.chatPhase != chat.PhaseIdle {
		t.Errorf("chatPhase = %v, want idle after restore", m.chatPhase)
	}
	for _, it := range m.transcript.Items() {
		if it.Streaming {
			t.Errorf("item %s still streaming after restore", it.ID)
		}
	}
}

// A failed restore posts a transient error notice and returns to idle (the
// error lives in the notice, not the phase), so the user can still type.
func TestModel_RestoreErrorShowsOnStatus(t *testing.T) {
	m := testModel(t)
	m.Update(historyLoadedMsg{err: errors.New("not found")})

	if m.chatPhase != chat.PhaseIdle {
		t.Fatalf("chatPhase = %v, want idle after failed restore", m.chatPhase)
	}
	if m.notice.Level != chat.NoticeError {
		t.Errorf("notice level = %v, want error", m.notice.Level)
	}
	if got := m.View().Content; !strings.Contains(got, "not found") {
		t.Errorf("status line missing restore error:\n%s", got)
	}
}

// A turn error posts an error notice while keeping PhaseError as the turn
// lifecycle state; clearing the notice leaves the phase untouched (the two are
// decoupled).
func TestModel_TurnErrorPostsNotice(t *testing.T) {
	m := testModel(t)
	m.feed(agent.Event{Kind: agent.EventError, Err: errors.New("boom")})
	if m.chatPhase != chat.PhaseError {
		t.Fatalf("chatPhase = %v, want error", m.chatPhase)
	}
	if m.notice.Level != chat.NoticeError || !strings.Contains(m.notice.Text, "boom") {
		t.Fatalf("notice = %+v, want error notice with boom", m.notice)
	}

	m.clearNotice()
	if !m.notice.Empty() {
		t.Errorf("notice = %+v, want cleared", m.notice)
	}
	if m.chatPhase != chat.PhaseError {
		t.Errorf("clearing the notice must not change the phase; got %v", m.chatPhase)
	}
}

// A stale expiry timer must not clear a newer notice; the matching one does.
func TestModel_NoticeExpirySeqGuard(t *testing.T) {
	m := testModel(t)
	m.showNotice(chat.Notice{Level: chat.NoticeError, Text: "first"}, time.Hour)
	staleSeq := m.noticeSeq
	m.showNotice(chat.Notice{Level: chat.NoticeInfo, Text: "second"}, time.Hour)

	m.Update(noticeExpiredMsg{seq: staleSeq})
	if m.notice.Text != "second" {
		t.Errorf("stale timer cleared the newer notice: %+v", m.notice)
	}
	m.Update(noticeExpiredMsg{seq: m.noticeSeq})
	if !m.notice.Empty() {
		t.Errorf("matching timer did not clear the notice: %+v", m.notice)
	}
}

// Binding the engine to a conversation shows its id immediately and puts the
// model in the loading phase on Init.
func TestModel_RestoringConversationLoadsOnInit(t *testing.T) {
	m := New(agent.New(nil, assistant.SendOptions{ConversationID: "conv-abc123"}))
	if m.convID != "conv-abc123" {
		t.Errorf("convID = %q, want conv-abc123", m.convID)
	}
	if cmd := m.Init(); cmd == nil {
		t.Error("Init returned no command while restoring")
	}
	if m.chatPhase != chat.PhaseLoading {
		t.Errorf("chatPhase = %v, want loading on Init", m.chatPhase)
	}
}

// Content kinds with no renderer yet must be ignored, not create empty blocks.
func TestModel_UnrenderedContentIgnored(t *testing.T) {
	m := testModel(t)
	m.feedMsg(assistant.Message{
		Role:      "assistant",
		MessageID: "m1",
		Content: assistant.Content{
			Type:      assistant.ContentDashboard,
			Dashboard: &assistant.DashboardPayload{Title: "gen"},
		},
	})
	if got := len(m.transcript.Items()); got != 0 {
		t.Errorf("want no items for a dashboard, got %d", got)
	}
}

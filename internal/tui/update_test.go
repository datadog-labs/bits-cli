package tui

import (
	"errors"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// testModel returns a sized model with a no-op engine (the backend is never
// called because these tests drive Update with events directly).
func testModel(t *testing.T) *Model {
	t.Helper()
	m := New(agent.New(nil, assistant.SendOptions{}))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !m.ready {
		t.Fatal("model not ready after WindowSizeMsg")
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

func TestModel_TurnDoneReturnsToReady(t *testing.T) {
	m := testModel(t)
	m.feedMsg(assistant.AssistantMessage("m1", assistant.TextContent("hi")))
	m.feed(agent.Event{Kind: agent.EventTurnDone})

	if got := m.View().Content; !strings.Contains(got, "ready") {
		t.Errorf("status line should show ready after turn done:\n%s", got)
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
	for _, want := range []string{"run_bash", "error", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q:\n%s", want, got)
		}
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

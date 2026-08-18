package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func TestModel_StreamsDeltasIntoView(t *testing.T) {
	m := testModel(t)
	m.feed(agent.Event{Kind: agent.EventDelta, ItemID: "msg:1", Role: assistant.RoleAssistant, Content: assistant.KindText, Text: "hello "})
	m.feed(agent.Event{Kind: agent.EventDelta, ItemID: "msg:1", Role: assistant.RoleAssistant, Content: assistant.KindText, Text: "world"})

	if got := m.View().Content; !strings.Contains(got, "hello world") {
		t.Errorf("view missing concatenated stream:\n%s", got)
	}
	if m.chatPhase != chat.PhaseStreaming {
		t.Errorf("chatPhase = %v, want streaming", m.chatPhase)
	}
}

func TestModel_TurnDoneReturnsToReady(t *testing.T) {
	m := testModel(t)
	m.feed(agent.Event{Kind: agent.EventDelta, ItemID: "msg:1", Role: assistant.RoleAssistant, Content: assistant.KindText, Text: "hi"})
	m.feed(agent.Event{Kind: agent.EventTurnDone})

	if got := m.View().Content; !strings.Contains(got, "ready") {
		t.Errorf("status line should show ready after turn done:\n%s", got)
	}
}

func TestModel_ToolAndErrorRender(t *testing.T) {
	m := testModel(t)
	m.feed(agent.Event{Kind: agent.EventTool, ItemID: "tool:1", Tool: agent.ToolCall{Name: "run_bash", Output: "ok", Status: "success"}})
	m.feed(agent.Event{Kind: agent.EventError, Err: errors.New("boom")})

	got := m.View().Content
	for _, want := range []string{"run_bash", "error", "boom"} {
		if !strings.Contains(got, want) {
			t.Errorf("view missing %q:\n%s", want, got)
		}
	}
}

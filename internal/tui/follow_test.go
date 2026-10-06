package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

func followTestModel() *Model {
	m := newShell()
	m.mode = ModeChat
	m.resize(60, 16)
	m.relayout()
	m.transcript.Blocks = []agent.Block{{
		ID:   agent.BlockID{Scope: agent.ScopeMessage, Key: "response"},
		Role: assistant.RoleAssistant, Kind: assistant.KindText,
		Markdown: &assistant.MarkdownPayload{Content: strings.Repeat("response row\n\n", 40)},
	}}
	m.syncTranscript()
	m.list.ScrollToBottom()
	return m
}

func TestFollowControlReturnsToStreamingTailWithoutGrowingView(t *testing.T) {
	m := followTestModel()
	m.View()
	if !m.follow.area.Empty() {
		t.Fatal("control shown at bottom")
	}
	m.list.ScrollBy(-4)
	top := m.list.VisibleSurface().Top
	m.chatPhase = chat.PhaseStreaming
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.transcript.Blocks[0].Markdown.Content += "\n\nnew output"
	m.transcript.Blocks[0].Rev++
	m.syncTranscript()
	view := m.View().Content
	if m.list.VisibleSurface().Top != top || m.list.Following() {
		t.Fatal("streaming moved the reading position")
	}
	if lipgloss.Height(view) != m.height || !strings.Contains(ansi.Strip(view), "New activity · ↓ Jump to latest") {
		t.Fatalf("missing control or changed view height:\n%s", ansi.Strip(view))
	}
	for _, width := range []int{12, 24, 60} {
		m.resize(width, 16)
		m.relayout()
		view = m.View().Content
		if m.follow.area.Empty() || lipgloss.Width(view) > width || lipgloss.Height(view) != m.height {
			t.Fatalf("width %d: missing target or incorrect view dimensions", width)
		}
	}
	m.chatPhase = chat.PhaseIdle
	if idle := ansi.Strip(m.View().Content); !strings.Contains(idle, "↓ Jump to latest") || strings.Contains(idle, "New activity") {
		t.Fatalf("completed response should keep a plain return control:\n%s", idle)
	}
	m.chatPhase = chat.PhaseStreaming
	view = m.View().Content
	area := m.follow.area
	contentY := area.Min.Y + 1 // the rounded border occupies the top and bottom rows.
	row := strings.Split(ansi.Strip(view), "\n")[contentY]
	if !strings.Contains(row, "Jump to latest") {
		t.Fatalf("target row does not match visible control: %q", row)
	}
	mouse := tea.Mouse{X: area.Min.X, Y: contentY, Button: tea.MouseLeft}
	m.Update(tea.MouseClickMsg(mouse))
	if m.selection.selecting() || m.list.Following() {
		t.Fatal("button press started selection or jumped before release")
	}
	m.Update(tea.MouseReleaseMsg(mouse))
	if !m.list.Following() || !m.list.AtBottom() {
		t.Fatal("button release did not resume following")
	}
	m.transcript.Blocks[0].Markdown.Content += "\n\nnext output"
	m.transcript.Blocks[0].Rev++
	m.syncTranscript()
	m.View()
	if !m.list.AtBottom() || !m.follow.area.Empty() {
		t.Fatal("subsequent output was not followed or control stayed visible")
	}
}

func TestFollowControlCancelsReleaseAndYieldsToOtherSurfaces(t *testing.T) {
	for _, action := range []string{"outside", "selection", "resize", "mode"} {
		t.Run(action, func(t *testing.T) {
			m := followTestModel()
			m.list.ScrollBy(-4)
			m.View()
			area := m.follow.area
			mouse := tea.Mouse{X: area.Min.X, Y: area.Min.Y, Button: tea.MouseLeft}
			m.Update(tea.MouseClickMsg(mouse))
			switch action {
			case "outside":
				mouse.X = 0
			case "selection":
				m.selection.begin(testSelectionFrame("abc", 3, 1), 0, 0)
			case "resize":
				m.resize(40, 16)
				m.relayout()
			case "mode":
				m.setMode(ModeStatus)
			}
			m.View()
			m.Update(tea.MouseReleaseMsg(mouse))
			if m.list.Following() || !m.follow.pressed.Empty() {
				t.Fatal("cancelled button gesture jumped or remained pressed")
			}
		})
	}
}

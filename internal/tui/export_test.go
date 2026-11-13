package tui

import (
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"

	tea "charm.land/bubbletea/v2"
)

func exportText(role assistant.Role, content string) agent.Block {
	return agent.Block{Role: role, Kind: assistant.KindText, Complete: true, Markdown: &assistant.MarkdownPayload{Content: content}}
}

func TestConversationMarkdown(t *testing.T) {
	table := "| a | b |\n|---|---|\n| 1 | 2 |"
	code := "```go\nfmt.Println(1)\n```"
	for _, tc := range []struct {
		name   string
		blocks []agent.Block
		user   string
		link   string
		want   string
		ok     bool
	}{
		{name: "empty", ok: false},
		{name: "only empty bodies", blocks: []agent.Block{exportText(assistant.RoleUser, "  "), exportText(assistant.RoleAssistant, "")}, ok: false},
		{
			name: "every message is prefixed and markdown kept",
			user: "  ",
			blocks: []agent.Block{
				exportText(assistant.RoleUser, "show **me**"),
				{Role: assistant.RoleAssistant, Kind: assistant.KindReasoning, Thinking: &assistant.ThinkingPayload{}},
				{Role: assistant.RoleAssistant, Kind: assistant.KindToolCall, Tool: &agent.ToolBlock{}},
				exportText(assistant.RoleAssistant, code),
				exportText(assistant.RoleAssistant, table),
				exportText(assistant.RoleUser, "thanks"),
				exportText(assistant.RoleAssistant, ""),
			},
			want: "**You:** show **me**\n\n**Bits:**\n\n" + code + "\n\n**Bits:**\n\n" + table + "\n\n**You:** thanks",
			ok:   true,
		},
		{
			name:   "user label",
			user:   " Ladi ",
			blocks: []agent.Block{exportText(assistant.RoleUser, "hi")},
			want:   "**Ladi:** hi",
			ok:     true,
		},
		{
			name: "charts",
			blocks: []agent.Block{
				{Role: assistant.RoleAssistant, Kind: assistant.KindWidget, Widget: &assistant.WidgetPayload{WidgetDef: &assistant.WidgetDefinition{Title: "CPU"}}},
				{Role: assistant.RoleAssistant, Kind: assistant.KindWidget, Widget: &assistant.WidgetPayload{Title: "Legacy"}},
				{Role: assistant.RoleAssistant, Kind: assistant.KindWidget, Widget: &assistant.WidgetPayload{WidgetDef: &assistant.WidgetDefinition{Title: " "}}},
			},
			want: "**Bits:** _[Chart: CPU]_\n\n**Bits:** _[Chart: Legacy]_\n\n**Bits:** _[Chart]_",
			ok:   true,
		},
		{
			name: "related entities and footer",
			blocks: []agent.Block{
				{
					Role: assistant.RoleUser, Kind: assistant.KindText, Complete: true,
					Markdown: &assistant.MarkdownPayload{Content: "why?"},
					Context: []assistant.ContextEntity{
						{Type: assistant.EntityDashboard, ID: "abc", Label: "Board", URL: "https://app.datadoghq.com/dashboard/abc"},
						{Type: assistant.EntityMonitor, ID: "123", URL: "https://app.datadoghq.com/monitors/123"},
						{Type: assistant.EntityService, ID: "https://not-a-url-field", Label: "no url"},
					},
				},
				{
					Role: assistant.RoleUser, Kind: assistant.KindText, Complete: true,
					Markdown: &assistant.MarkdownPayload{Content: " "},
					Context:  []assistant.ContextEntity{{ID: "x", URL: "https://example.com/x"}},
				},
			},
			link: "https://app.datadoghq.com/ask/c1",
			want: "**You:** why?\n_Related: [Board](https://app.datadoghq.com/dashboard/abc), [123](https://app.datadoghq.com/monitors/123)_\n\n> _🟪 Conversation from Bits AI ([link](https://app.datadoghq.com/ask/c1))_",
			ok:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := conversationMarkdown(tc.blocks, tc.user, tc.link)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
			if fences := strings.Count(got, "```"); fences%2 != 0 {
				t.Fatalf("unbalanced code fences in %q", got)
			}
			for _, line := range strings.Split(got, "\n") {
				if strings.HasPrefix(line, "**") && strings.Contains(line, ":** ```") {
					t.Fatalf("fence hidden behind a label: %q", line)
				}
			}
		})
	}
}

func TestExportCommand(t *testing.T) {
	m := New(agent.New(&spyBackend{t: t}, agent.Config{}))
	_, _ = m.dispatchCommand("export", "")
	if latestNotice(m).Level != chat.NoticeWarn || !strings.Contains(latestNotice(m).Text, "no messages") {
		t.Fatalf("empty notice = %+v", latestNotice(m))
	}

	m.state.Blocks = []agent.Block{exportText(assistant.RoleUser, "hi"), exportText(assistant.RoleAssistant, "hello")}
	m.op = operation{kind: opTurn, run: 1}
	_, _ = m.dispatchCommand("export", "")
	if !strings.Contains(latestNotice(m).Text, "before using /export") {
		t.Fatalf("busy notice = %+v", latestNotice(m))
	}

	m.op = operation{}
	m.currentUser.Name = "Ladi"
	if !clipboardContains(t, m, "**Ladi:** hi\n\n**Bits:** hello") {
		t.Fatal("clipboard payload mismatch")
	}
}

func clipboardContains(t *testing.T, m *Model, want string) bool {
	t.Helper()
	_, cmd := m.dispatchCommand("export", "")
	expected := tea.SetClipboard(want)()
	for _, msg := range flattenMsgs(t, cmd) {
		if msg == expected {
			return true
		}
	}
	return false
}

// A resumed conversation exports the same turns, once each and in order, as
// the live conversation that produced them.
func TestExportNewAndResumedConversationMatch(t *testing.T) {
	backend := fake.New()
	live := New(agent.New(backend, agent.Config{}))
	for _, prompt := range []string{`say("alpha")`, `say("bravo")`} {
		setConversationInput(live, prompt)
		_, _ = live.submit()
		drainConversationRemote(t, live)
	}
	want, ok := conversationMarkdown(live.state.Blocks, "", "")
	if !ok || strings.Index(want, "alpha") > strings.Index(want, "bravo") {
		t.Fatalf("live export = %q", want)
	}

	resumed := New(agent.New(backend, agent.Config{}))
	_, _ = resumed.Update(runResumeCmd(t, resumed.openConversationPicker()))
	_, _ = resumed.Update(runResumeCmd(t, resumed.startConversationSwitch(live.state.ConversationID)))
	got, _ := conversationMarkdown(resumed.state.Blocks, "", "")
	if got != want || strings.Count(got, "**Bits:** alpha") != 1 {
		t.Fatalf("resumed export = %q, want %q", got, want)
	}
}

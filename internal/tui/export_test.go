package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
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
			name: "block-level bodies get the label on its own line",
			blocks: []agent.Block{
				exportText(assistant.RoleAssistant, code),
				exportText(assistant.RoleUser, "# Title\ntext"),
				exportText(assistant.RoleAssistant, table),
				exportText(assistant.RoleAssistant, "- one\n- two"),
				exportText(assistant.RoleAssistant, "#hashtag and 3.5 is not a list"),
				exportText(assistant.RoleUser, "plain *text*"),
			},
			link: "https://app.datadoghq.com/ask/c1",
			want: "**Bits:**\n\n" + code +
				"\n\n**You:**\n\n# Title\ntext" +
				"\n\n**Bits:**\n\n" + table +
				"\n\n**Bits:**\n\n- one\n- two" +
				"\n\n**Bits:** #hashtag and 3.5 is not a list" +
				"\n\n**You:** plain *text*" +
				"\n\n> _🟪 Conversation from Bits AI ([link](https://app.datadoghq.com/ask/c1))_",
			ok: true,
		},
		{
			name: "second-line block constructs",
			blocks: []agent.Block{
				exportText(assistant.RoleAssistant, "Title\n==="),
				exportText(assistant.RoleAssistant, "Title\ncontinued\n==="),
				exportText(assistant.RoleAssistant, "Title\n---"),
				exportText(assistant.RoleAssistant, "a | b\n--- | ---\n1 | 2"),
				exportText(assistant.RoleAssistant, "~~~\nraw ```\n~~~"),
				exportText(assistant.RoleUser, "after"),
				exportText(assistant.RoleAssistant, "one\ntwo"),
			},
			link: "https://app.datadoghq.com/ask/c1",
			want: "**Bits:**\n\nTitle\n===" +
				"\n\n**Bits:**\n\nTitle\ncontinued\n===" +
				"\n\n**Bits:**\n\nTitle\n---" +
				"\n\n**Bits:**\n\na | b\n--- | ---\n1 | 2" +
				"\n\n**Bits:**\n\n~~~\nraw ```\n~~~" +
				"\n\n**You:** after" +
				"\n\n**Bits:** one\ntwo" +
				"\n\n> _🟪 Conversation from Bits AI ([link](https://app.datadoghq.com/ask/c1))_",
			ok: true,
		},
		{
			name: "inline look-alikes stay on the label line",
			blocks: []agent.Block{
				exportText(assistant.RoleAssistant, "```x``` inline"),
				exportText(assistant.RoleAssistant, "**x**"),
				exportText(assistant.RoleAssistant, "-1"),
				exportText(assistant.RoleAssistant, "1234567890. x"),
			},
			want: "**Bits:** ```x``` inline\n\n**Bits:** **x**\n\n**Bits:** -1\n\n**Bits:** 1234567890. x",
			ok:   true,
		},
		{
			name: "breaks, items, and indented fences are blocks",
			blocks: []agent.Block{
				exportText(assistant.RoleAssistant, "***"),
				exportText(assistant.RoleAssistant, "* * *"),
				exportText(assistant.RoleAssistant, "1. item"),
				exportText(assistant.RoleAssistant, "````\n```\n````"),
				exportText(assistant.RoleAssistant, "x\n\n   ~~~\ncode\n   ~~~"),
			},
			link: "https://app.datadoghq.com/ask/c1",
			want: "**Bits:**\n\n***\n\n**Bits:**\n\n* * *\n\n**Bits:**\n\n1. item" +
				"\n\n**Bits:**\n\n````\n```\n````\n\n**Bits:** x\n\n   ~~~\ncode\n   ~~~" +
				"\n\n> _🟪 Conversation from Bits AI ([link](https://app.datadoghq.com/ask/c1))_",
			ok: true,
		},
		{
			name: "indentation before the first line is trimmed",
			blocks: []agent.Block{
				exportText(assistant.RoleAssistant, "   ~~~\ncode\n~~~"),
				exportText(assistant.RoleAssistant, "    indented code"),
			},
			want: "**Bits:**\n\n~~~\ncode\n~~~\n\n**Bits:** indented code",
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
			if !ok {
				return
			}
			// Parse as the renderer does: every speaker label must start a
			// top-level paragraph and the footer must be the last block, so
			// no body swallowed a label, a later turn, or the footer.
			doc := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList)).Parser().Parse(text.NewReader([]byte(got)))
			labels := 0
			for node := doc.FirstChild(); node != nil; node = node.NextSibling() {
				if strong, isStrong := node.FirstChild().(*ast.Emphasis); node.Kind() == ast.KindParagraph && isStrong && strong.Level == 2 {
					labels++
				}
			}
			if want := strings.Count(got, ":**"); labels != want {
				t.Fatalf("%d of %d speaker labels start a top-level paragraph in %q", labels, want, got)
			}
			if tc.link != "" && doc.LastChild().Kind() != ast.KindBlockquote {
				t.Fatalf("footer is not the last top-level block in %q", got)
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

// /copy and /export take the conversation as it shows, also an answer
// another session's turn streamed in.
func TestCopyAndExportIncludeAnotherSessionsAnswer(t *testing.T) {
	f := fake.New()
	id, err := f.PushConversation(context.Background(), "", `say("earlier")`)
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(f, agent.Config{ConversationID: id}))
	_, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = m.initChat()
	drainConversationRemote(t, m)
	pumpUntilFree(t, m)
	sent := make(chan error, 1)
	go func() {
		_, err := f.Send(context.Background(), `say("remote answer")`, assistant.SendOptions{ConversationID: id}, func(assistant.AssistantResponse) error { return nil })
		sent <- err
	}()
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	for !hasTextBlock(m.state.Blocks, "remote answer") || m.sendBlocked() {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	_, cmd := m.dispatchCommand("copy", "")
	copied := tea.SetClipboard("remote answer")()
	if !slices.Contains(flattenMsgs(t, cmd), copied) {
		t.Fatalf("/copy = %v, want the other session's answer", flattenMsgs(t, cmd))
	}
	_, cmd = m.dispatchCommand("export", "")
	exported := false
	for _, msg := range flattenMsgs(t, cmd) {
		exported = exported || strings.Contains(fmt.Sprint(msg), "remote answer")
	}
	if !exported {
		t.Fatal("/export left out the other session's answer")
	}
}

// A failure that reuses the answer's message ID shows apart from the answer,
// once, live and after a replay; /copy and /export leave it out.
func TestTurnErrorStaysApartFromTheAnswer(t *testing.T) {
	f := fake.New()
	id, err := f.PushConversation(context.Background(), "", `say("ready")`)
	if err != nil {
		t.Fatal(err)
	}
	live := New(agent.New(f, agent.Config{ConversationID: id}))
	_, _ = live.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(live, `load("notices.star", "turn_error"); turn_error()`)
	_, _ = live.submit()
	pumpUntilFree(t, live)
	for !slices.ContainsFunc(live.state.Blocks, func(b agent.Block) bool { return b.Error }) {
		_, _ = live.Update(runConversationCmd(t, waitEvent(live.events)))
	}
	resumed := New(agent.New(f, agent.Config{ConversationID: id}))
	_, _ = resumed.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = resumed.initChat()
	drainConversationRemote(t, resumed)
	pumpUntilFree(t, resumed)
	for name, m := range map[string]*Model{"live": live, "replayed": resumed} {
		doc := ansi.Strip(m.list.Document())
		if n := strings.Count(doc, "Token rate limit exceeded"); n != 1 {
			t.Errorf("%s: failure shows %d times:\n%s", name, n, doc)
		}
		if text, ok := latestCopyableAssistantResponse(m.state.Blocks); !ok || text != "Partial answer before the model hit its token rate limit." {
			t.Errorf("%s: /copy = %q", name, text)
		}
		if text, _ := conversationMarkdown(m.state.Blocks, "", ""); strings.Contains(text, "exceeded") {
			t.Errorf("%s: /export holds the failure:\n%s", name, text)
		}
	}
}

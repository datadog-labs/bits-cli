package tui

import (
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/browser"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

// exportConversation copies the loaded transcript as Markdown.
func (m *Model) exportConversation() tea.Cmd {
	link := ""
	if m.engine != nil && strings.TrimSpace(m.state.ConversationID) != "" {
		if target, err := browser.ConversationURL(m.engine.WebOrigin(), m.state.ConversationID); err == nil {
			link = target
		}
	}
	text, ok := conversationMarkdown(m.state.Blocks, m.currentUser.Name, link)
	if !ok {
		return m.postNotice(notice(chat.NoticeWarn, nil, "This conversation has no messages to export."))
	}
	return tea.Batch(
		m.postNotice(notice(chat.NoticeInfo, nil, "Copied conversation to clipboard.")),
		tea.SetClipboard(text),
	)
}

// conversationMarkdown serializes user prompts, assistant answers, and chart
// placeholders, each prefixed with its speaker, dropping thinking, tools, and
// empty bodies. The user label falls back to "You".
func conversationMarkdown(blocks []agent.Block, userLabel, link string) (string, bool) {
	userLabel = strings.TrimSpace(userLabel)
	if userLabel == "" {
		userLabel = "You"
	}
	var turns []string
	for _, block := range blocks {
		if turn := exportTurn(block, userLabel); turn != "" {
			turns = append(turns, turn)
		}
	}
	if len(turns) == 0 {
		return "", false
	}
	if link != "" {
		turns = append(turns, "> _🟪 Conversation from Bits AI ([link]("+link+"))_")
	}
	return strings.Join(turns, "\n\n"), true
}

func exportTurn(block agent.Block, userLabel string) string {
	switch block.Role {
	case assistant.RoleUser:
		if block.Kind != assistant.KindText || block.Markdown == nil {
			return ""
		}
		body := strings.TrimSpace(ansi.Strip(block.Markdown.Content))
		if body == "" {
			return ""
		}
		turn := withSpeaker(userLabel, body)
		if related := relatedLinks(block.Context); related != "" {
			turn += "\n" + related
		}
		return turn
	case assistant.RoleAssistant:
		switch block.Kind {
		case assistant.KindText:
			if block.Markdown == nil {
				return ""
			}
			if body := strings.TrimSpace(ansi.Strip(block.Markdown.Content)); body != "" {
				return withSpeaker("Bits", body)
			}
		case assistant.KindWidget:
			return withSpeaker("Bits", chartPlaceholder(block.Widget))
		case assistant.KindUnknown, assistant.KindReasoning, assistant.KindToolCall, assistant.KindToolResult,
			assistant.KindDashboard, assistant.KindProgress, assistant.KindTurnMarker, assistant.KindStop, assistant.KindInternal:
		}
	case assistant.RoleSystem:
	}
	return ""
}

func chartPlaceholder(widget *assistant.WidgetPayload) string {
	title := ""
	if widget != nil {
		title = widget.Title
		if widget.WidgetDef != nil {
			title = widget.WidgetDef.Title
		}
	}
	if title = strings.TrimSpace(title); title == "" {
		return "_[Chart]_"
	}
	return "_[Chart: " + title + "]_"
}

func relatedLinks(entities []assistant.ContextEntity) string {
	var links []string
	for _, entity := range entities {
		if entity.URL == "" {
			continue
		}
		label := entity.Label
		if label == "" {
			label = entity.ID
		}
		links = append(links, "["+label+"]("+entity.URL+")")
	}
	if len(links) == 0 {
		return ""
	}
	return "_Related: " + strings.Join(links, ", ") + "_"
}

// blockStart matches a first line that Markdown reads as a block only at the
// start of a line: a code fence, heading, table, quote, list item, or
// thematic break.
var blockStart = regexp.MustCompile(`^(` + "```" + `|~~~|#{1,6}(\s|$)|\||>|[-*+](\s|$)|\d{1,9}[.)](\s|$)|(\*\s*){3,}$|(-\s*){3,}$|(_\s*){3,}$)`)

// withSpeaker prefixes a turn with its speaker on the same line, as the web UI
// does. A body opening with a block construct would stop being one behind
// the label, so the label then gets its own paragraph.
func withSpeaker(label, body string) string {
	firstLine, _, _ := strings.Cut(body, "\n")
	if blockStart.MatchString(firstLine) {
		return "**" + label + ":**\n\n" + body
	}
	return "**" + label + ":** " + body
}

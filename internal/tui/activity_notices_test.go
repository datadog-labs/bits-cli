package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

func TestActivityReplayPreservesNoticePosition(t *testing.T) {
	for _, publish := range []bool{false, true} {
		t.Run(map[bool]string{false: "before submit", true: "live update"}[publish], func(t *testing.T) {
			m := New(agent.New(&spyBackend{t: t}, agent.Config{ConversationID: "one"}))
			m.state.ConversationID = "one"
			m.activityConversationID = "one"
			m.activitySnapshotReady = true
			transcript := agent.NewTranscript()
			local := transcript.AppendUser("my prompt", nil)
			m.state.Blocks = []agent.Block{local}
			m.postNotice(chat.Notice{Level: chat.NoticeInfo, Text: "session notice"})
			user := assistant.AssistantMessage("user", assistant.TextContent("my prompt"))
			user.Role = "user"
			answer := assistant.AssistantMessage("answer", assistant.TextContent("first answer"))
			m.activityReplay = agent.NewActivityReplay(user, answer)
			m.activityNeedsSync = true
			if publish {
				m.publishActivityTranscript()
			} else if !m.syncActivityTranscript() {
				t.Fatal("sync failed")
			}
			if m.notices[0].After != agent.BlockIDOf(user) {
				t.Fatalf("notice anchor = %v", m.notices[0].After)
			}
			m.activityReplay.Append(assistant.AssistantMessage("later", assistant.TextContent("later answer")))
			m.publishActivityTranscript()
			m.syncTranscript()
			m.list.SetWidth(80)
			doc := ansi.Strip(m.list.Document())
			previous := -1
			for _, text := range []string{"my prompt", "session notice", "first answer", "later answer"} {
				at := strings.Index(doc, text)
				if at <= previous {
					t.Fatalf("%q out of order:\n%s", text, doc)
				}
				previous = at
			}
		})
	}
}

func TestActivityNoticeReanchoringBoundsRepeatedPrompts(t *testing.T) {
	user := func(id string) agent.Block {
		msg := assistant.AssistantMessage(id, assistant.TextContent("repeat"))
		msg.Role = "user"
		transcript := agent.NewTranscript()
		b, _ := transcript.AppendMessage(msg)
		return b
	}
	first, second := user("first"), user("second")
	transcript := agent.NewTranscript()
	local := transcript.AppendUser("repeat", nil)
	m := newModelWithSpy(t)
	m.state.Blocks = []agent.Block{first, local}
	m.postNotice(chat.Notice{Level: chat.NoticeInfo, Text: "after second prompt"})
	m.reanchorActivityNotices([]agent.Block{first, second})
	if m.notices[0].After != second.ID {
		t.Fatal("notice matched an earlier identical prompt")
	}

	m.state.Blocks = []agent.Block{local, second}
	m.notices[0].After = local.ID
	m.reanchorActivityNotices([]agent.Block{second})
	if m.notices[0].After != (agent.BlockID{}) {
		t.Fatal("notice matched a later prompt when its own prompt was absent")
	}
}

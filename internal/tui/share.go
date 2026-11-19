package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/browser"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/conversations"
)

const shareTimeout = 30 * time.Second

// maxShareDays is the longest share the server accepts.
const maxShareDays = 1095

const shareUsage = "Use /share, /share off, or /share <days>d with 1 to 1095 days."

// shareOptions are the web's two sharing choices, worded the same.
var shareOptions = [...]components.Option{
	{Label: "Private", Description: "Only you can view this conversation. Anyone who previously had access will not be able to view it."},
	{Label: "Shared", Description: "Anyone in your organization with this link can view all current and future messages."},
}

const sharedOption = 1 // the index of Shared in shareOptions

type shareResultMsg struct {
	generation uint64
	epoch      uint64
	in         assistant.ShareConversationInput
	until      *time.Time
	err        error
}

// startShare runs /share: without an argument it asks who may view the
// conversation; "off" makes it private; "<days>d" shares it for that long.
// Only the owner may change sharing, so others can only copy an active link.
func (m *Model) startShare(argument string) tea.Cmd {
	conversationID := m.state.ConversationID
	if !agent.ValidConversationID(conversationID) {
		return m.postNotice(notice(chat.NoticeWarn, nil, "Start a conversation before using /share."))
	}
	if m.state.ReadOnly {
		if until, ok := conversations.ActiveShare(m.state.SharedUntil, time.Now()); ok && argument == "" {
			return m.copyShareLink(conversationID, until)
		}
		return m.postNotice(notice(chat.NoticeWarn, nil, "Only the conversation's owner can change its sharing."))
	}
	if argument == "" {
		m.openSharePrompt(conversationID)
		return nil
	}
	if strings.EqualFold(argument, "off") {
		return m.share(assistant.ShareConversationInput{ConversationID: conversationID})
	}
	value, hasUnit := strings.CutSuffix(strings.ToLower(argument), "d")
	days, err := strconv.Atoi(value)
	if !hasUnit || err != nil || days < 1 || days > maxShareDays {
		return m.postNotice(notice(chat.NoticeWarn, nil, shareUsage))
	}
	return m.share(assistant.ShareConversationInput{ConversationID: conversationID, Shared: true, TTLDays: &days})
}

// openSharePrompt offers the two choices with the current one marked and
// Shared highlighted. The state can miss a change made elsewhere, such as on
// the web, so Private is always sent. Shared on an active share only copies
// the link, because sharing again would restart its expiry.
// NOTE: a share revoked elsewhere still reads as active, so its copied link
// fails closed until the conversation is loaded again.
func (m *Model) openSharePrompt(conversationID string) {
	until, shared := conversations.ActiveShare(m.state.SharedUntil, time.Now())
	options := shareOptions
	current := 0
	if shared {
		current = sharedOption
		options[sharedOption].Description += " Expires " + shareDate(until) + ". Enter copies the link."
	}
	options[current].Label += " (current)"
	form := components.NewChoicePrompt("Share conversation", options[:], sharedOption)
	epoch := m.conversationEpoch
	openCommandPrompt(m, form, func(choice int) tea.Cmd {
		if epoch != m.conversationEpoch {
			return nil
		}
		if choice == sharedOption && shared {
			return m.copyShareLink(conversationID, until)
		}
		return m.share(assistant.ShareConversationInput{ConversationID: conversationID, Shared: choice == sharedOption})
	})
}

// share saves the sharing change in the background; the result reports it.
func (m *Model) share(in assistant.ShareConversationInput) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), shareTimeout)
	if !m.begin(opShare, cancel) {
		return nil
	}
	generation, epoch := m.op.gen, m.conversationEpoch
	engine := m.engine
	return func() tea.Msg {
		// Cancellation may precede execution of this queued command.
		err := ctx.Err()
		var until *time.Time
		if err == nil {
			until, err = engine.ShareConversation(ctx, in)
		}
		return shareResultMsg{generation: generation, epoch: epoch, in: in, until: until, err: err}
	}
}

func (m *Model) applyShareResult(msg shareResultMsg) tea.Cmd {
	if !m.op.matches(opShare, msg.generation) {
		return nil
	}
	interrupted := m.op.interrupted()
	cmd := m.endOperation()
	if interrupted || msg.epoch != m.conversationEpoch {
		return cmd
	}
	switch {
	case msg.err != nil:
		m.postNotice(noticeForError("could not change sharing", msg.err))
	case msg.in.Shared:
		cmd = tea.Batch(cmd, m.copyShareLink(msg.in.ConversationID, *msg.until))
	default:
		m.postNotice(notice(chat.NoticeInfo, nil, "This conversation is private. Its link no longer works for others."))
	}
	return cmd
}

// copyShareLink copies the conversation's link and also shows it, because
// the terminal may ignore clipboard writes without telling us.
func (m *Model) copyShareLink(conversationID string, until time.Time) tea.Cmd {
	target, err := browser.ConversationURL(m.engine.WebOrigin(), conversationID)
	shared := "Shared with your organization until " + shareDate(until) + "."
	if err != nil {
		return m.postNotice(notice(chat.NoticeWarn, err, "%s Could not build its link.", shared))
	}
	return tea.Batch(
		m.postNotice(notice(chat.NoticeInfo, nil, "%s Share link: %s", shared, target)),
		tea.SetClipboard(target),
	)
}

// statusSharing describes who can view the conversation, or "" before it is
// saved.
// NOTE: it reflects the last load or /share, so a change made on the web
// meanwhile shows only once the conversation is loaded again. Only the
// history request reads sharing back, and it carries the whole transcript.
func (m *Model) statusSharing() string {
	if !agent.ValidConversationID(m.state.ConversationID) {
		return ""
	}
	if until, ok := conversations.ActiveShare(m.state.SharedUntil, time.Now()); ok {
		return "shared with your organization until " + shareDate(until)
	}
	return "private"
}

func shareDate(t time.Time) string { return t.Local().Format("Jan 2, 2006") }

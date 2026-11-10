package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

// submit routes slash commands through their active-operation policy. A prompt
// starts a turn when idle, or else joins the engine's queue; Enter on an
// empty draft sends a paused queue. A turn starts only once the previous
// one's end is read, so one turn is shown at a time. The user block is added
// by the engine (it owns the transcript), so it arrives as the turn's first
// event.
func (m *Model) submit() (tea.Model, tea.Cmd) {
	raw := m.editor.Value()
	text := strings.TrimSpace(raw)
	attachments := m.editor.Attachments()
	if text == "" && len(attachments) == 0 {
		if m.editor.QueuePaused() && !m.op.busy() {
			if m.activityBlocksSubmission() {
				return m, m.activityBlockedNotice()
			}
			return m, m.startTurn(m.engine.SendQueued, agent.TurnInput{})
		}
		return m, nil
	}
	if len(attachments) == 0 && isExitWord(raw) {
		m.openExitCard()
		return m, nil
	}
	if text == "" {
		text = " "
	}

	var skillInvocation *agent.SkillInvocation
	if name, arguments, ok := parseClientSkillInvocation(raw); ok {
		skillInvocation = &agent.SkillInvocation{Name: name, Arguments: arguments}
		text = raw
	} else if name, argument, ok := parseCommand(raw); ok {
		m.editor.Reset()
		m.stopEntitySearch()
		return m.dispatchCommand(name, argument)
	}
	// Only a turn accepts prompts behind it; other operations and a pending
	// follow-up keep the draft.
	if m.op.then != thenNothing || (m.op.busy() && m.op.kind != opTurn) {
		return m, nil
	}
	// Once the menu has loaded, an unknown name keeps the draft for correction.
	// Otherwise the engine resolves the name and fails the turn if it is unknown.
	if skillInvocation != nil && m.skillMenu.skills != nil {
		if _, known := m.skillMenu.skills[skillInvocation.Name]; !known {
			return m, m.postNotice(notice(chat.NoticeWarn, nil, "Unknown client skill: %s", escape.Inline(skillInvocation.Name)))
		}
	}
	startsNow := !m.op.busy() && len(m.state.Queued) == 0
	if startsNow && m.activityBlocksSubmission() {
		return m, m.activityBlockedNotice()
	}
	if startsNow && !m.syncActivityTranscript() {
		return m, m.postNotice(notice(chat.NoticeWarn, nil, "Couldn't refresh the conversation. Try again."))
	}
	in := agent.TurnInput{Message: text, Skill: skillInvocation, Context: contextFromAttachments(attachments)}
	m.editor.Reset()
	closeFileSearch := m.stopCompletionSearches()
	// Behind a turn, or a queue still waiting, the prompt joins the queue.
	if !startsNow {
		if err := m.engine.Queue(in); err != nil {
			return m, tea.Batch(closeFileSearch, m.postNotice(noticeForError("", err)))
		}
		m.state = m.engine.State() // the event that publishes it follows
		return m, closeFileSearch
	}
	return m, tea.Batch(closeFileSearch, m.startTurn(m.engine.StartTurn, in))
}

// startTurn starts a turn through start and shows it, or its failure.
func (m *Model) startTurn(start func(context.Context, agent.TurnInput) (agent.RunID, error), in agent.TurnInput) tea.Cmd {
	in.Tools, in.OnDeny = m.tools, agent.DenyContinue
	ctx, cancel := context.WithCancel(context.Background())
	if !m.begin(opTurn, cancel) {
		return nil
	}
	// Queued prompts are local turns too: this TUI owns their outcome.
	m.localTurnOwned = true
	m.localTurnRecoverable = false
	m.activityLocalTurn = localTurnSent
	m.localTurnInput = in
	run, err := start(ctx, in)
	m.chatPhase = chat.PhaseWaiting
	failed := m.watchRun(run, err)
	// Submitting always jumps to the tail and re-engages auto-follow, so the
	// user sees their message and the incoming reply even if they had scrolled up.
	m.list.ScrollToBottom()
	return failed
}

// syncQueue mirrors the engine's queue into the editor. Only a turn drains
// prompts; other exclusive operations leave them waiting for Enter.
func (m *Model) syncQueue() {
	items := make([]editor.QueuedPrompt, len(m.state.Queued))
	for i, q := range m.state.Queued {
		items[i] = editor.QueuedPrompt{ID: uint64(q.ID), HistoryEntry: editorPrompt(q.Message, entities(q.Context))}
	}
	m.editor.SetQueue(items, m.op.kind != opTurn)
}

func entities(context *assistant.AssistantContext) []assistant.ContextEntity {
	if context == nil {
		return nil
	}
	return context.Entities
}

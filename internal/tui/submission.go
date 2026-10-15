package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

// submit handles local commands immediately. Agent prompts join the FIFO while
// a turn runs; restore and session transitions leave the draft untouched.
func (m *Model) submit() (tea.Model, tea.Cmd) {
	p := m.editor.Snapshot()
	if strings.TrimSpace(p.Text) == "" && len(p.Attachments) == 0 {
		if !m.op.busy() {
			m.editor.ResumeQueue()
			return m, m.dispatchQueued()
		}
		return m, nil
	}
	if _, _, skill := parseClientSkillInvocation(p.Text); !skill {
		if name, argument, ok := parseCommand(p.Text); ok {
			m.editor.Reset()
			m.stopEntitySearch()
			return m.dispatchCommand(name, argument)
		}
	}
	if m.op.then != thenNothing || (m.op.busy() && m.op.kind != opTurn) {
		return m, nil
	}
	if cmd, valid := m.validatePrompt(p); !valid {
		return m, cmd
	}
	if m.op.busy() || m.editor.QueueLen() > 0 {
		m.editor.Enqueue(p)
		m.editor.Reset()
		closeSearch := m.stopCompletionSearches()
		if !m.op.busy() {
			m.editor.ResumeQueue()
		}
		return m, tea.Batch(closeSearch, m.dispatchQueued())
	}
	m.editor.Reset()
	return m, tea.Batch(m.stopCompletionSearches(), m.startPrompt(p))
}

func (m *Model) validatePrompt(p editor.Prompt) (tea.Cmd, bool) {
	if name, _, ok := parseClientSkillInvocation(p.Text); ok && m.skillMenu.skills != nil {
		if _, known := m.skillMenu.skills[name]; !known {
			return m.postNotice(notice(chat.NoticeWarn, nil, "Unknown client skill: %s", escape.Inline(name))), false
		}
	}
	return nil, true
}

// dispatchQueued is called only at a released operation boundary or an explicit
// idle submission. It never reads or resets the draft being composed.
func (m *Model) dispatchQueued() tea.Cmd {
	if m.op.busy() || m.engine == nil || m.editor.QueuePaused() {
		return nil
	}
	prompts := m.editor.QueuedPrompts()
	if len(prompts) == 0 {
		return nil
	}
	p, count := queuedBatch(prompts)
	if cmd, valid := m.validatePrompt(p); !valid {
		m.editor.PauseQueue()
		return cmd
	}
	images := 0
	for _, a := range p.Attachments {
		if a.Type == string(assistant.EntityImageURL) {
			images++
		}
	}
	if images > imageinput.MaxImages {
		m.editor.PauseQueue()
		return m.postNotice(notice(chat.NoticeWarn, nil,
			"Queued prompts contain %d images; a batch supports at most %d. Remove queued prompts before resuming.", images, imageinput.MaxImages))
	}
	m.editor.ConsumeQueued(count)
	return m.startPrompt(p)
}

func (m *Model) startPrompt(p editor.Prompt) tea.Cmd {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		text = " "
	}
	var skill *agent.SkillInvocation
	if name, arguments, ok := parseClientSkillInvocation(p.Text); ok {
		skill = &agent.SkillInvocation{Name: name, Arguments: arguments}
		text = p.Text
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := m.engine.StartTurn(ctx, agent.TurnInput{
		Message: text, Skill: skill, Tools: m.tools,
		Context: contextFromAttachments(p.Attachments), OnDeny: agent.DenyContinue,
	})
	wait := m.begin(opTurn, events, cancel)
	m.chatPhase = chat.PhaseWaiting
	m.list.ScrollToBottom()
	return wait
}

// queuedBatch joins consecutive ordinary prompts into one user message. Explicit
// skill invocations remain boundaries: TurnInput carries one skill and its own
// arguments, which must not absorb unrelated prompts or other skill invocations.
// This policy is independent of queue storage and can evolve with skill support.
func queuedBatch(prompts []editor.Prompt) (editor.Prompt, int) {
	var batch editor.Prompt
	var texts []string
	type attachmentIdentity struct{ kind, id string }
	seen := make(map[attachmentIdentity]bool)
	count := 0
	for _, p := range prompts {
		if _, _, skill := parseClientSkillInvocation(p.Text); skill {
			if count == 0 {
				return p, 1
			}
			break
		}
		count++
		if strings.TrimSpace(p.Text) != "" {
			texts = append(texts, p.Text)
		}
		for _, a := range p.Attachments {
			identity := attachmentIdentity{kind: a.Type, id: a.ID}
			if !seen[identity] {
				seen[identity] = true
				batch.Attachments = append(batch.Attachments, a)
			}
		}
	}
	batch.Text = strings.Join(texts, "\n\n")
	return batch, count
}

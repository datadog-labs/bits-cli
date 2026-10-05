package tui

import (
	"context"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/editor"
	"github.com/DataDog/bits-cli/internal/tui/escape"
)

type clientSkillsResultMsg struct {
	generation uint64
	epoch      uint64
	skills     []agent.SkillSummary
	err        error
}

// skillMenu records which engine and conversation the menu was loaded for.
// Discovery is a bounded local scan, so a failed load is reported, not retried;
// /skill:<name> still works because the engine resolves names itself.
type skillMenu struct {
	engine  *agent.Engine
	epoch   uint64
	started bool
	// skills is nil until a load succeeds for this conversation.
	skills map[string]agent.SkillSummary
	task   task
}

// syncClientSkills observes conversation identity and loads the menu once per
// conversation.
func (m *Model) syncClientSkills() tea.Cmd {
	if m.engine == nil || m.op.kind == opRestore {
		return nil
	}
	if m.skillMenu.started && m.skillMenu.engine == m.engine && m.skillMenu.epoch == m.conversationEpoch {
		return nil
	}
	return m.loadClientSkillMenu()
}

func (m *Model) loadClientSkillMenu() tea.Cmd {
	m.skillMenu.skills = nil
	m.editor.SetCommands(commandCompletionSpecs())
	m.skillMenu.engine, m.skillMenu.epoch, m.skillMenu.started = m.engine, m.conversationEpoch, true
	// The engine bounds discovery; this context only abandons a superseded load.
	ctx, generation := m.skillMenu.task.start(context.Background(), 0)
	engine, epoch := m.engine, m.conversationEpoch
	return func() tea.Msg {
		var skills []agent.SkillSummary
		var err error
		if engine != nil {
			skills, err = engine.ClientSkills(ctx)
		}
		return clientSkillsResultMsg{generation: generation, epoch: epoch, skills: skills, err: err}
	}
}

func (m *Model) applyClientSkills(msg clientSkillsResultMsg) tea.Cmd {
	if msg.generation != m.skillMenu.task.gen || msg.epoch != m.conversationEpoch {
		return nil
	}
	m.skillMenu.task.done()
	if msg.err != nil {
		return m.postNotice(notice(chat.NoticeWarn, msg.err, "Could not load client skill suggestions. You can still invoke a skill with /skill:<name>."))
	}
	m.skillMenu.skills = make(map[string]agent.SkillSummary, len(msg.skills))
	specs := commandCompletionSpecs()
	// The registry already supplies a stable, sorted list.
	for _, skill := range msg.skills {
		m.skillMenu.skills[skill.Name] = skill
		specs = append(specs, editor.CommandSpec{Name: "skill:" + skill.Name, Aliases: []string{skill.Name}, Detail: escape.Inline(skill.Description)})
	}
	m.editor.SetCommands(specs)
	return nil
}

// parseClientSkillInvocation preserves the entire argument remainder, including
// separator whitespace, instead of using the native command parser.
func parseClientSkillInvocation(raw string) (name, arguments string, ok bool) {
	const prefix = "/skill:"
	if !strings.HasPrefix(raw, prefix) {
		return "", "", false
	}
	remainder := raw[len(prefix):]
	end := strings.IndexFunc(remainder, unicode.IsSpace)
	if end < 0 {
		end = len(remainder)
	}
	return remainder[:end], remainder[end:], true
}

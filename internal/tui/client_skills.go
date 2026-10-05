package tui

import (
	"context"
	"strings"
	"time"
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

type skillMenuState uint8

const (
	skillMenuIdle skillMenuState = iota
	skillMenuLoading
	skillMenuReady
	skillMenuFailed
)

const maxSkillMenuAttempts = 3

type skillMenu struct {
	engine   *agent.Engine
	epoch    uint64
	state    skillMenuState
	attempts int
	task     task
}

type clientSkillsRetryMsg struct {
	generation uint64
	epoch      uint64
}

// syncClientSkills observes conversation identity. Failed loads retry through a
// delayed message, never through every render or input event.
func (m *Model) syncClientSkills() tea.Cmd {
	if m.engine == nil || m.op.kind == opRestore {
		return nil
	}
	if m.skillMenu.engine != m.engine || m.skillMenu.epoch != m.conversationEpoch {
		m.skillMenu.engine = m.engine
		m.skillMenu.epoch = m.conversationEpoch
		m.skillMenu.state = skillMenuIdle
		m.skillMenu.attempts = 0
	}
	if m.skillMenu.state != skillMenuIdle {
		return nil
	}
	return m.loadClientSkillMenu()
}

func (m *Model) loadClientSkillMenu() tea.Cmd {
	m.clientSkills = nil
	m.editor.SetCommands(commandCompletionSpecs())
	m.skillMenu.state = skillMenuLoading
	m.skillMenu.attempts++
	ctx, generation := m.skillMenu.task.start(context.Background(), 5*time.Second)
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
		m.skillMenu.state = skillMenuFailed
		if m.skillMenu.attempts < maxSkillMenuAttempts {
			return tea.Tick(time.Duration(m.skillMenu.attempts)*250*time.Millisecond, func(time.Time) tea.Msg {
				return clientSkillsRetryMsg{generation: msg.generation, epoch: msg.epoch}
			})
		}
		return m.postNotice(notice(chat.NoticeWarn, msg.err, "Could not load client skill suggestions. You can still invoke a skill with /skill:<name>."))
	}
	m.skillMenu.state = skillMenuReady
	m.clientSkills = make(map[string]agent.SkillSummary, len(msg.skills))
	specs := commandCompletionSpecs()
	// The registry already supplies a stable, sorted list.
	for _, skill := range msg.skills {
		m.clientSkills[skill.Name] = skill
		specs = append(specs, editor.CommandSpec{Name: "skill:" + skill.Name, Aliases: []string{skill.Name}, Detail: escape.Inline(skill.Description)})
	}
	m.editor.SetCommands(specs)
	return nil
}

func (m *Model) retryClientSkills(msg clientSkillsRetryMsg) tea.Cmd {
	if msg.generation != m.skillMenu.task.gen || msg.epoch != m.conversationEpoch || m.skillMenu.state != skillMenuFailed || m.skillMenu.attempts >= maxSkillMenuAttempts {
		return nil
	}
	return m.loadClientSkillMenu()
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

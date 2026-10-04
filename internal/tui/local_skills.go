package tui

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/editor"
	"github.com/DataDog/bits-cli/internal/tui/escape"
)

type localSkillsResultMsg struct {
	generation uint64
	epoch      uint64
	skills     []agent.LocalSkill
}

func (m *Model) refreshLocalSkills() tea.Cmd {
	m.localSkills = nil
	m.editor.SetCommands(commandCompletionSpecs())
	ctx, generation := m.localSkillsTask.start(context.Background(), 5*time.Second)
	engine, epoch := m.engine, m.conversationEpoch
	return func() tea.Msg {
		var skills []agent.LocalSkill
		if engine != nil {
			skills = engine.DiscoverLocalSkills(ctx)
		}
		return localSkillsResultMsg{generation: generation, epoch: epoch, skills: skills}
	}
}

func (m *Model) applyLocalSkills(msg localSkillsResultMsg) {
	if msg.generation != m.localSkillsTask.gen || msg.epoch != m.conversationEpoch {
		return
	}
	m.localSkillsTask.done()
	m.localSkills = make(map[string]agent.LocalSkill, len(msg.skills))
	specs := commandCompletionSpecs()
	for _, skill := range msg.skills {
		m.localSkills[skill.Name] = skill
	}
	names := make([]string, 0, len(m.localSkills))
	for name := range m.localSkills {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		specs = append(specs, editor.CommandSpec{Name: "skill:" + name, Aliases: []string{name}, Detail: escape.Inline(m.localSkills[name].Description)})
	}
	m.editor.SetCommands(specs)
}

// parseLocalSkillInvocation preserves the entire argument remainder, including
// separator whitespace, instead of using the native command parser.
func parseLocalSkillInvocation(raw string) (name, arguments string, ok bool) {
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

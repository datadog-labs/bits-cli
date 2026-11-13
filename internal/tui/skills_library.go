package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/editor"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
)

const (
	skillsLibraryCommand = "skills-library"
	skillsLibraryPrefix  = "skills-library/"
)

type skillsLibraryResultMsg struct {
	generation uint64
	skills     []assistant.Skill
	err        error
}

type skillsLibraryEntry struct {
	short, canonical, description string
}

// Every picker open reloads the catalog; the last good list stays visible.
type skillsLibrary struct {
	engine *agent.Engine
	// entries is nil until a load succeeds.
	entries []skillsLibraryEntry
	// hidden counts untypeable names.
	hidden  int
	err     error
	loading bool
	task    task
	active  bool
}

func (l *skillsLibrary) lookup(short string) (skillsLibraryEntry, bool) {
	for _, entry := range l.entries {
		if entry.short == short {
			return entry, true
		}
	}
	return skillsLibraryEntry{}, false
}

func (m *Model) resetSkillsLibrary() {
	m.skillsLibrary.task.stop()
	m.skillsLibrary = skillsLibrary{engine: m.engine, task: m.skillsLibrary.task}
	m.pushSkillsLibrary()
}

func (m *Model) syncSkillsLibrary() tea.Cmd {
	if m.skillsLibrary.engine != m.engine {
		m.resetSkillsLibrary()
	}
	active := m.editor.CommandPickerActive()
	opened := active && !m.skillsLibrary.active
	m.skillsLibrary.active = active
	if !opened {
		return nil
	}
	return m.loadSkillsLibrary()
}

func (m *Model) loadSkillsLibrary() tea.Cmd {
	if m.engine == nil || m.searchesBlocked() {
		return nil
	}
	if m.skillsLibrary.engine != m.engine {
		m.resetSkillsLibrary()
	}
	ctx, generation := m.skillsLibrary.task.start(context.Background(), 0)
	m.skillsLibrary.loading = true
	m.pushSkillsLibrary()
	engine := m.engine
	return func() tea.Msg {
		skills, err := engine.ServerSkills(ctx)
		return skillsLibraryResultMsg{generation: generation, skills: skills, err: err}
	}
}

func (m *Model) applySkillsLibrary(msg skillsLibraryResultMsg) {
	if msg.generation != m.skillsLibrary.task.gen {
		return
	}
	m.skillsLibrary.task.done()
	m.skillsLibrary.loading = false
	m.skillsLibrary.err = msg.err
	if msg.err == nil {
		entries := make([]skillsLibraryEntry, 0, len(msg.skills))
		hidden := 0
		seen := map[string]struct{}{}
		for _, skill := range msg.skills {
			if skill.Source != string(assistant.SkillSourceSkillsLibrary) {
				continue
			}
			short := strings.TrimPrefix(skill.Name, skillsLibraryPrefix)
			if short == "" || strings.ContainsFunc(short, unicode.IsSpace) || escape.Inline(short) != short {
				hidden++
				continue
			}
			if _, dup := seen[short]; dup {
				continue
			}
			seen[short] = struct{}{}
			entries = append(entries, skillsLibraryEntry{short: short, canonical: skill.Name, description: skill.Description})
		}
		m.skillsLibrary.entries, m.skillsLibrary.hidden = entries, hidden
	}
	m.pushSkillsLibrary()
}

func (m *Model) pushSkillsLibrary() {
	lib := &m.skillsLibrary
	items := make([]editor.Candidate, 0, len(lib.entries))
	for _, entry := range lib.entries {
		items = append(items, editor.Candidate{
			Kind: editor.CandidateCommandArgument, ID: entry.short,
			Label: entry.short, Detail: escape.Inline(entry.description),
			Insert: "/" + skillsLibraryCommand + ":" + entry.short,
		})
	}
	status := ""
	switch {
	case lib.loading && lib.entries == nil:
		status = "Loading Skills Library…"
	case lib.err != nil:
		status = "Skills Library unavailable"
	case lib.hidden == 1:
		status = "1 skill hidden: name can't be typed in Bits"
	case lib.hidden > 1:
		status = fmt.Sprintf("%d skills hidden: names can't be typed in Bits", lib.hidden)
	}
	m.editor.SetCommandPicker(skillsLibraryCommand, items, status)
}

func (m *Model) openSkillsLibrary() tea.Cmd {
	value := m.editor.Value()
	if !strings.HasPrefix(value, "/"+skillsLibraryCommand+" ") && !strings.HasPrefix(value, "/"+skillsLibraryCommand+":") {
		m.editor.SetValue("/" + skillsLibraryCommand + " ")
	}
	m.editor.ReopenCompletion()
	load := m.loadSkillsLibrary()
	m.skillsLibrary.active = m.editor.CommandPickerActive()
	return load
}

// The argument remainder is kept exactly as typed.
func parseSkillsLibraryInvocation(raw string) (name, arguments string, ok bool) {
	remainder, typed := strings.CutPrefix(raw, "/"+skillsLibraryCommand+":")
	if !typed {
		return "", "", false
	}
	end := strings.IndexFunc(remainder, unicode.IsSpace)
	if end < 0 {
		end = len(remainder)
	}
	return remainder[:end], remainder[end:], true
}

// sentInvocation recognizes the sent form "/<short name><arguments>", which
// prompt history recalls.
func (l *skillsLibrary) sentInvocation(raw string) (short, arguments string, ok bool) {
	rest, slash := strings.CutPrefix(raw, "/")
	if !slash {
		return "", "", false
	}
	end := strings.IndexFunc(rest, unicode.IsSpace)
	if end < 0 {
		end = len(rest)
	}
	// NOTE: a library skill whose short name equals a native command replays
	// from history as that command; native wins.
	if _, native := lookupCommand(strings.ToLower(rest[:end])); native {
		return "", "", false
	}
	for _, entry := range l.entries {
		if entry.short == rest[:end] {
			return entry.short, rest[end:], true
		}
	}
	return "", "", false
}

// The override enables the skill for this turn: the web uses per-user
// settings Bits lacks, so a skill off by default could otherwise never run.
// NOTE: a skill removed after the last load is ignored by the server, which
// then receives only the text.
func (m *Model) skillsLibraryTurn(name, arguments string) (agent.TurnInput, tea.Cmd, bool) {
	lib := &m.skillsLibrary
	if entry, ok := lib.lookup(name); ok {
		return agent.TurnInput{
			Message:        "/" + entry.short + arguments,
			SkillOverrides: []assistant.SkillOverride{{Name: entry.canonical, Source: assistant.SkillSourceSkillsLibrary, Enabled: true}},
		}, nil, true
	}
	switch {
	case lib.entries == nil && lib.err != nil && !lib.loading:
		err := lib.err
		return agent.TurnInput{}, tea.Batch(m.loadSkillsLibrary(), m.postNotice(notice(chat.NoticeWarn, err, "Could not load the Skills Library. Local skills still work with /skill:<name>."))), false
	case lib.entries == nil:
		var load tea.Cmd
		if !lib.loading {
			load = m.loadSkillsLibrary()
		}
		return agent.TurnInput{}, tea.Batch(load, m.postNotice(notice(chat.NoticeInfo, nil, "Skills Library is still loading. Try again in a moment."))), false
	}
	return agent.TurnInput{}, m.postNotice(notice(chat.NoticeWarn, nil, "Skills Library skill not found: %s. Type /skills-library to refresh suggestions.", escape.Inline(name))), false
}

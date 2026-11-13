package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

type libraryBackend struct {
	skillRecordingBackend
	mu     sync.Mutex
	skills []assistant.Skill
	err    error
	calls  int
}

func (b *libraryBackend) ListSkills(context.Context) ([]assistant.Skill, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return slices.Clone(b.skills), b.err
}

func (b *libraryBackend) set(skills []assistant.Skill, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.skills, b.err = skills, err
}

func (b *libraryBackend) listCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

var testLibrarySkills = []assistant.Skill{
	{Name: "coding", Source: "assistant", Description: "assistant skill"},
	{Name: "code-search", Source: "datadog_mcp", Description: "mcp skill"},
	{Name: "skills-library/clear-writing", Source: "skills_library", Description: "Rewrite text plainly"},
	{Name: "skills-library/slo-review", Source: "skills_library", Description: "Review SLO burn"},
	{Name: "skills-library/bad name", Source: "skills_library", Description: "untypeable"},
}

func newLibraryModel(t *testing.T, backend *libraryBackend) *Model {
	t.Helper()
	if backend.skills == nil && backend.err == nil {
		backend.skills = testLibrarySkills
	}
	m := New(agent.New(backend, agent.Config{}))
	m.editor.Focus()
	return m
}

// runLibraryLoad executes cmd, including batched children, and applies the
// Skills Library result it produces. Other commands, such as cursor blinks,
// may block and are left running.
func runLibraryLoad(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	results := make(chan skillsLibraryResultMsg, 1)
	var run func(tea.Cmd)
	run = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		go func() {
			switch msg := cmd().(type) {
			case tea.BatchMsg:
				for _, child := range msg {
					run(child)
				}
			case skillsLibraryResultMsg:
				results <- msg
			}
		}()
	}
	run(cmd)
	select {
	case msg := <-results:
		m.applySkillsLibrary(msg)
	case <-t.Context().Done():
		t.Fatal("no Skills Library load")
	}
}

func handleKey(m *Model, code rune) tea.Cmd {
	_, cmd := m.handleEditorKey(tea.KeyPressMsg{Code: code})
	return cmd
}

func shortNames(m *Model) []string {
	names := []string{}
	for _, entry := range m.skillsLibrary.entries {
		names = append(names, entry.short)
	}
	return names
}

func TestSkillsLibraryMainMenuHasOneEntry(t *testing.T) {
	count := 0
	for _, spec := range commandCompletionSpecs() {
		if strings.HasPrefix(spec.Name, "skills-library") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("skills-library menu rows = %d, want 1", count)
	}
}

func TestSkillsLibraryPickerFlow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		key   rune
	}{
		{name: "enter on menu row", input: "/skills-lib", key: tea.KeyEnter},
		{name: "tab on menu row", input: "/skills-lib", key: tea.KeyTab},
		{name: "typed space form", input: "/skills-library "},
		{name: "typed colon form", input: "/skills-library:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &libraryBackend{}
			m := newLibraryModel(t, backend)
			m.editor.Update(tea.PasteMsg{Content: tc.input})
			cmd := handleKey(m, tea.KeyEnd)
			if tc.key != 0 {
				cmd = tea.Batch(cmd, handleKey(m, tc.key))
			}
			if !m.editor.CommandPickerActive() || m.op.run != 0 {
				t.Fatalf("picker active = %v, run = %d, value %q", m.editor.CommandPickerActive(), m.op.run, m.editor.Value())
			}
			runLibraryLoad(t, m, cmd)
			if want := []string{"clear-writing", "slo-review"}; !slices.Equal(shortNames(m), want) {
				t.Fatalf("entries = %v, want %v", shortNames(m), want)
			}
			if got := m.skillsLibrary.entries[0].canonical; got != "skills-library/clear-writing" {
				t.Fatalf("canonical = %q", got)
			}
			m.editor.Update(tea.PasteMsg{Content: "clear"})
			handleKey(m, tea.KeyEnter)
			if got := m.editor.Value(); got != "/skills-library:clear-writing " || m.op.run != 0 {
				t.Fatalf("value = %q, run = %d", got, m.op.run)
			}
		})
	}
}

func TestSkillsLibraryEscapeKeepsDraftAndReopenRefreshes(t *testing.T) {
	backend := &libraryBackend{}
	m := newLibraryModel(t, backend)
	m.editor.Update(tea.PasteMsg{Content: "/skills-library draft"})
	runLibraryLoad(t, m, handleKey(m, tea.KeyEnd))
	handleKey(m, tea.KeyEscape)
	if m.editor.MenuOpen() || m.editor.Value() != "/skills-library draft" {
		t.Fatalf("after Esc: open %v, value %q", m.editor.MenuOpen(), m.editor.Value())
	}
	backend.set([]assistant.Skill{{Name: "skills-library/fresh", Source: "skills_library"}}, nil)
	cmd := handleKey(m, tea.KeyEnter)
	if !m.editor.MenuOpen() || m.editor.Value() != "/skills-library draft" || m.op.run != 0 {
		t.Fatalf("reopen: open %v, value %q, run %d", m.editor.MenuOpen(), m.editor.Value(), m.op.run)
	}
	runLibraryLoad(t, m, cmd)
	if backend.listCalls() != 2 || !slices.Equal(shortNames(m), []string{"fresh"}) {
		t.Fatalf("calls = %d, entries = %v", backend.listCalls(), shortNames(m))
	}
}

// followRun waits for the model's current run to finish.
func followRun(t *testing.T, m *Model) {
	t.Helper()
	if m.op.run == 0 {
		t.Fatal("no turn started")
	}
	for event := range agent.Follow(m.events, m.op.run) {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
}

func TestSkillsLibraryInvocationSendsCanonicalNameAndOverride(t *testing.T) {
	backend := &libraryBackend{}
	m := newLibraryModel(t, backend)
	runLibraryLoad(t, m, m.openSkillsLibrary())
	m.editor.Reset()
	m.editor.Update(tea.PasteMsg{Content: "/skills-library:clear-writing fix it"})
	_, _ = m.submit()
	followRun(t, m)
	want := assistant.SkillOverride{Name: "skills-library/clear-writing", Source: assistant.SkillSourceSkillsLibrary, Enabled: true}
	if backend.message != "/clear-writing fix it" || !slices.Contains(backend.opts.SkillOverrides, want) {
		t.Fatalf("message = %q, overrides = %+v", backend.message, backend.opts.SkillOverrides)
	}

	// History recalls the sent form; resubmitting it invokes the skill again.
	_ = m.endOperation()
	m.state = m.engine.State()
	backend.message, backend.opts = nil, assistant.SendOptions{}
	handleKey(m, tea.KeyUp)
	if got := m.editor.Value(); got != "/clear-writing fix it" {
		t.Fatalf("recalled %q", got)
	}
	_, _ = m.submit()
	followRun(t, m)
	if backend.message != "/clear-writing fix it" || !slices.Contains(backend.opts.SkillOverrides, want) {
		t.Fatalf("replay message = %q, overrides = %+v", backend.message, backend.opts.SkillOverrides)
	}
}

func TestSkillsLibraryInvocationFeedbackKeepsDraft(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		load    bool
		input   string
		want    string
		reloads bool
	}{
		{name: "loading", input: "/skills-library:clear-writing", want: "still loading", reloads: true},
		{name: "load failed", err: errors.New("boom"), load: true, input: "/skills-library:clear-writing", want: "Could not load the Skills Library", reloads: true},
		{name: "not found", load: true, input: "/skills-library:gone", want: "not found: gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newLibraryModel(t, &libraryBackend{err: tc.err})
			if tc.load {
				runLibraryLoad(t, m, m.openSkillsLibrary())
			}
			m.editor.SetValue(tc.input)
			_, _ = m.submit()
			if m.op.run != 0 || m.editor.Value() != tc.input {
				t.Fatalf("run %d, value %q", m.op.run, m.editor.Value())
			}
			if !strings.Contains(lastNotice(m).Text, tc.want) {
				t.Fatalf("notice = %q, want %q", lastNotice(m).Text, tc.want)
			}
			if m.skillsLibrary.loading != tc.reloads {
				t.Fatalf("loading = %v, want %v", m.skillsLibrary.loading, tc.reloads)
			}
		})
	}
}

func TestSkillsLibraryCountsHiddenNames(t *testing.T) {
	m := newLibraryModel(t, &libraryBackend{})
	runLibraryLoad(t, m, m.openSkillsLibrary())
	if !slices.Equal(shortNames(m), []string{"clear-writing", "slo-review"}) || m.skillsLibrary.hidden != 1 {
		t.Fatalf("entries = %v, hidden = %v", shortNames(m), m.skillsLibrary.hidden)
	}
	if view := m.editor.MenuView(); !strings.Contains(view, "1 skill hidden") {
		t.Fatalf("menu = %q", view)
	}
}

func TestLocalSkillsWorkWhenLibraryFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	installTestSkill(t, root, "review", false)
	backend := &libraryBackend{err: errors.New("unavailable")}
	m := New(agent.New(backend, agent.Config{ClientSkills: env.NewSkills(root, nil)}))
	m.editor.Focus()
	runLibraryLoad(t, m, m.openSkillsLibrary())
	m.editor.Reset()
	m.editor.Update(tea.PasteMsg{Content: "/skill:review go"})
	_, _ = m.submit()
	followRun(t, m)
	if backend.message != "/skill:review go" {
		t.Fatalf("message = %q", backend.message)
	}
}

func TestSkillsLibraryHistoryReplay(t *testing.T) {
	for _, tc := range []struct {
		name, canonical string
	}{
		{name: "prefixed", canonical: "skills-library/clear-writing"},
		{name: "unprefixed", canonical: "plain-skill"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &libraryBackend{skills: []assistant.Skill{
				{Name: tc.canonical, Source: "skills_library"},
				{Name: "status", Source: "skills_library"}, // collides with a native command
			}}
			m := newLibraryModel(t, backend)
			runLibraryLoad(t, m, m.openSkillsLibrary())
			short := strings.TrimPrefix(tc.canonical, "skills-library/")
			m.editor.SetValue("/skills-library:" + short + " go")
			_, _ = m.submit()
			followRun(t, m)
			_ = m.endOperation()
			m.state = m.engine.State()
			backend.message, backend.opts = nil, assistant.SendOptions{}

			handleKey(m, tea.KeyUp)
			if got, want := m.editor.Value(), "/"+short+" go"; got != want {
				t.Fatalf("recalled %q, want %q", got, want)
			}
			_, _ = m.submit()
			followRun(t, m)
			want := assistant.SkillOverride{Name: tc.canonical, Source: assistant.SkillSourceSkillsLibrary, Enabled: true}
			if backend.message != "/"+short+" go" || !slices.Contains(backend.opts.SkillOverrides, want) {
				t.Fatalf("replay message = %q, overrides = %+v", backend.message, backend.opts.SkillOverrides)
			}
			_ = m.endOperation()

			// A library name equal to a native command never shadows it.
			if _, _, ok := m.skillsLibrary.sentInvocation("/status"); ok {
				t.Fatal("/status resolved to a library skill")
			}
		})
	}
}

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

type skillRecordingBackend struct {
	message any
	opts    assistant.SendOptions
}

func (b *skillRecordingBackend) Send(_ context.Context, message any, opts assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.message, b.opts = message, opts
	return "skill-conversation", nil
}

func installTestSkill(t *testing.T, root, name string, userOnly bool) string {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: Run " + name + "\n"
	if userOnly {
		content += "disable-model-invocation: true\n"
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content+"---\nSKILL BODY"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalSkillSlashInvocation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		skillName  string
		input      string
		completion bool
		userOnly   bool
		tab        bool
	}{
		{name: "typed", input: "/skill:review   Fix README.md\nKeep CASE\n   "},
		{name: "user only", input: "/skill:review Fix README.md", userOnly: true},
		{name: "completion", input: "/skill:rev", completion: true, userOnly: true},
		{name: "bare name completion", input: "/review", completion: true, userOnly: true},
		{name: "bare prefix completion", input: "/rev", completion: true},
		{name: "tab with arguments", input: "/skill:rev", completion: true, tab: true},
		{name: "bare prefix tab with arguments", input: "/rev", completion: true, tab: true},
		{name: "namespaced typed", skillName: "figma:something", input: "/skill:figma:something Keep CASE"},
		{name: "namespaced completion", skillName: "figma:something", input: "/figma:some", completion: true, userOnly: true},
		{name: "namespaced tab", skillName: "figma:something", input: "/figma:some", completion: true, tab: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			skillName := tc.skillName
			if skillName == "" {
				skillName = "review"
			}
			path := installTestSkill(t, root, skillName, tc.userOnly)
			backend := &skillRecordingBackend{}
			m := New(agent.New(backend, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
			m.applyLocalSkills(m.refreshLocalSkills()().(localSkillsResultMsg))
			m.editor.Focus()
			m.editor.Update(tea.PasteMsg{Content: tc.input})
			if tc.completion && !m.editor.MenuOpen() {
				t.Fatal("skill completion did not open")
			}
			if tc.tab {
				_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyTab})
				m.editor.Update(tea.PasteMsg{Content: "Check README.md"})
				_, _ = m.submit()
			} else if tc.completion {
				_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			} else {
				_, _ = m.submit()
			}
			if m.op.events == nil {
				t.Fatal("skill invocation did not start a turn")
			}
			for event := range m.op.events {
				if event.Err != nil {
					t.Fatal(event.Err)
				}
			}
			want := tc.input
			if tc.completion {
				want = "/skill:" + skillName
			}
			if tc.tab {
				want = "/skill:" + skillName + " Check README.md"
			}
			if backend.message != want {
				t.Fatalf("message = %q, want %q", backend.message, want)
			}
			got := backend.opts.CustomUserContext
			if !strings.Contains(got, path) || !strings.Contains(got, "explicitly invoked") || !strings.Contains(got, "<instructions>SKILL BODY</instructions>") {
				t.Fatalf("invocation context = %q", got)
			}
			if !strings.Contains(got, `base-directory="`+filepath.Dir(path)+`"`) {
				t.Fatalf("missing reference base: %q", got)
			}
			if tc.userOnly && strings.Contains(got, `<skill name="`+skillName+`"`) {
				t.Fatalf("user-only skill reached automatic catalog: %q", got)
			}
		})
	}
}

func TestLocalSkillBuiltinCollisionsAndBusyGuard(t *testing.T) {
	m := newModelWithSpy(t)
	m.applyLocalSkills(localSkillsResultMsg{skills: []agent.LocalSkill{
		{Name: "new"}, {Name: "exit"}, {Name: "permissions"}, {Name: "review", Description: "Review \x1b[2J"},
	}})
	for _, name := range []string{"new", "exit", "permissions"} {
		if _, exists := m.localSkills[name]; !exists {
			t.Fatalf("skill named %s missing from registry", name)
		}
		if _, builtin := lookupCommand(name); !builtin {
			t.Fatalf("builtin %s no longer registered", name)
		}
	}
	m.editor.Update(tea.PasteMsg{Content: "/skill:rev"})
	if !strings.Contains(m.editor.MenuView(), "␛[2J") {
		t.Fatal("skill description controls were not escaped")
	}
	m.op = operation{kind: opTurn, events: make(chan agent.Event)}
	m.editor.SetValue("/skill:review Keep CASE")
	_, cmd := m.submit()
	if cmd != nil || m.editor.Value() != "/skill:review Keep CASE" {
		t.Fatal("busy skill invocation should remain in composer")
	}
}

func TestLocalSkillRefreshRejectsStaleResults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	path := installTestSkill(t, root, "review", true)
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
	old := m.refreshLocalSkills()().(localSkillsResultMsg)
	m.applyLocalSkills(old)
	if _, ok := m.localSkills["review"]; !ok {
		t.Fatal("missing user-only skill")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cmd := m.refreshLocalSkills()
	m.applyLocalSkills(old)
	if len(m.localSkills) != 0 {
		t.Fatal("stale result restored removed skill")
	}
	m.applyLocalSkills(cmd().(localSkillsResultMsg))
	if len(m.localSkills) != 0 {
		t.Fatal("removed skill survived rescan")
	}
	old.generation = m.localSkillsTask.gen
	m.conversationEpoch++
	m.applyLocalSkills(old)
	if len(m.localSkills) != 0 {
		t.Fatal("old conversation result restored skill")
	}
}

func TestParseLocalSkillInvocationPreservesArguments(t *testing.T) {
	for _, arguments := range []string{"", "  Fix README.md\n\tKeep CASE  ", "\nmultiline\n", "\tMixed Case"} {
		name, got, ok := parseLocalSkillInvocation("/skill:review" + arguments)
		if !ok || name != "review" || got != arguments {
			t.Fatalf("parse = (%q, %q, %t), want arguments %q", name, got, ok, arguments)
		}
	}
	for _, raw := range []string{" /skill:review", "explain /skill:review", "/review"} {
		if _, _, ok := parseLocalSkillInvocation(raw); ok {
			t.Fatalf("accepted ordinary input %q", raw)
		}
	}
}

func TestLocalSkillInvocationPreservesAttachments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	installTestSkill(t, root, "review", true)
	backend := &skillRecordingBackend{}
	m := New(agent.New(backend, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
	m.applyLocalSkills(m.refreshLocalSkills()().(localSkillsResultMsg))
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/skill:review @check"})
	_ = m.syncEntitySearch()
	m.applyEntitySearchResult(entitySearchResultMsg{
		generation: m.entitySearchTask.gen,
		query:      "check",
		response: assistant.SearchEntitiesResponse{
			SearchFlowID: "flow-1",
			Entities:     []assistant.SearchEntity{{CandidateID: "candidate-1", EntityID: "checkout-api", EntityType: "service", Name: "checkout-api"}},
		},
	})
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.editor.Attachments()) != 1 {
		t.Fatal("entity completion did not attach")
	}
	m.editor.Update(tea.PasteMsg{Content: " Inspect CASE\n  preserve spacing  "})
	want := m.editor.Value()
	m.editor.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	for range 3 {
		m.editor.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	}
	if !m.editor.MenuOpen() {
		t.Fatal("skill completion did not reopen at first token")
	}
	_, _ = m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.op.events == nil {
		t.Fatal("skill turn did not start")
	}
	for event := range m.op.events {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
	}
	if backend.message != want || backend.opts.Context == nil || len(backend.opts.Context.Entities) != 1 || backend.opts.Context.Entities[0].ID != "checkout-api" {
		t.Fatalf("message = %q, context = %+v", backend.message, backend.opts.Context)
	}
	if len(m.editor.Attachments()) != 0 {
		t.Fatal("submitted attachments remained in editor")
	}
}

func TestNewConversationRefreshesLocalSkillMenu(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	path := installTestSkill(t, root, "review", true)
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
	m.applyLocalSkills(m.refreshLocalSkills()().(localSkillsResultMsg))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	installTestSkill(t, root, "new", true)
	for _, msg := range flattenMsgs(t, m.startNewConversation()) {
		if result, ok := msg.(localSkillsResultMsg); ok {
			m.applyLocalSkills(result)
		}
	}
	if _, old := m.localSkills["review"]; old {
		t.Fatal("removed skill survived new conversation")
	}
	if _, found := m.localSkills["new"]; !found {
		t.Fatal("new skill missing after conversation reset")
	}
	m.editor.Focus()
	m.editor.Update(tea.PasteMsg{Content: "/skill:n"})
	if selected, ok := m.editor.SelectedCommand(); !ok || selected != "skill:new" {
		t.Fatalf("selection = %q, %t", selected, ok)
	}
	m.editor.Reset()
	m.editor.Update(tea.PasteMsg{Content: "/n"})
	if selected, ok := m.editor.SelectedCommand(); !ok || selected != "new" {
		t.Fatalf("native selection = %q, %t", selected, ok)
	}
}

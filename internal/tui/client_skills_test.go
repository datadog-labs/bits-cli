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
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

type skillRecordingBackend struct {
	message any
	opts    assistant.SendOptions
	err     error
}

func (b *skillRecordingBackend) Send(_ context.Context, message any, opts assistant.SendOptions, _ func(assistant.AssistantResponse) error) (string, error) {
	b.message, b.opts = message, opts
	return "skill-conversation", b.err
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

func TestClientSkillSlashInvocation(t *testing.T) {
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
			if tc.completion {
				m.applyClientSkills(m.loadClientSkillMenu()().(clientSkillsResultMsg))
			}
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

func TestClientSkillBuiltinCollisionsAndBusyGuard(t *testing.T) {
	m := newModelWithSpy(t)
	m.applyClientSkills(clientSkillsResultMsg{skills: []agent.SkillSummary{
		{Name: "new"}, {Name: "exit"}, {Name: "permissions"}, {Name: "review", Description: "Review \x1b[2J"},
	}})
	for _, name := range []string{"new", "exit", "permissions"} {
		if _, exists := m.clientSkills[name]; !exists {
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

func TestClientSkillMenuRejectsStaleResults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	path := installTestSkill(t, root, "review", true)
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
	old := m.loadClientSkillMenu()().(clientSkillsResultMsg)
	m.applyClientSkills(old)
	if _, ok := m.clientSkills["review"]; !ok {
		t.Fatal("missing user-only skill")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := m.engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	m.conversationEpoch++
	cmd := m.syncClientSkills()
	m.applyClientSkills(old)
	if len(m.clientSkills) != 0 {
		t.Fatal("stale result restored removed skill")
	}
	m.applyClientSkills(cmd().(clientSkillsResultMsg))
	if len(m.clientSkills) != 0 {
		t.Fatal("removed skill survived rescan")
	}
	old.generation = m.skillMenu.task.gen
	m.conversationEpoch++
	m.applyClientSkills(old)
	if len(m.clientSkills) != 0 {
		t.Fatal("old conversation result restored skill")
	}
}

func TestParseClientSkillInvocationPreservesArguments(t *testing.T) {
	for _, arguments := range []string{"", "  Fix README.md\n\tKeep CASE  ", "\nmultiline\n", "\tMixed Case"} {
		name, got, ok := parseClientSkillInvocation("/skill:review" + arguments)
		if !ok || name != "review" || got != arguments {
			t.Fatalf("parse = (%q, %q, %t), want arguments %q", name, got, ok, arguments)
		}
	}
	for _, raw := range []string{" /skill:review", "explain /skill:review", "/review"} {
		if _, _, ok := parseClientSkillInvocation(raw); ok {
			t.Fatalf("accepted ordinary input %q", raw)
		}
	}
}

func TestClientSkillInvocationPreservesAttachments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	installTestSkill(t, root, "review", true)
	backend := &skillRecordingBackend{}
	m := New(agent.New(backend, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
	m.applyClientSkills(m.loadClientSkillMenu()().(clientSkillsResultMsg))
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

func TestNewConversationRefreshesClientSkillMenu(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	path := installTestSkill(t, root, "review", true)
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
	m.applyClientSkills(m.loadClientSkillMenu()().(clientSkillsResultMsg))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	installTestSkill(t, root, "new", true)
	m.startNewConversation()
	_, update := m.Update(nil)
	for _, msg := range flattenMsgs(t, update) {
		if result, ok := msg.(clientSkillsResultMsg); ok {
			m.applyClientSkills(result)
		}
	}
	if _, old := m.clientSkills["review"]; old {
		t.Fatal("removed skill survived new conversation")
	}
	if _, found := m.clientSkills["new"]; !found {
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

func TestClientSkillMenuWaitsForRestoreAndLoadsOncePerConversation(t *testing.T) {
	m := newModelWithSpy(t)
	m.op.kind = opRestore
	if cmd := m.syncClientSkills(); cmd != nil {
		t.Fatal("discovery started before history installation could reset the registry")
	}
	m.op.kind = opIdle
	cmd := m.syncClientSkills()
	if cmd == nil {
		t.Fatal("restore completion did not load the menu")
	}
	m.applyClientSkills(cmd().(clientSkillsResultMsg))
	if cmd := m.syncClientSkills(); cmd != nil {
		t.Fatal("ordinary updates reload the menu")
	}
	m.conversationEpoch++
	cmd = m.syncClientSkills()
	if cmd == nil {
		t.Fatal("new conversation did not load the menu")
	}
	m.applyClientSkills(cmd().(clientSkillsResultMsg))
}

func TestClientSkillMenuReportsFailureOnceAndAcceptsEmptySuccess(t *testing.T) {
	m := newModelWithSpy(t)
	defer m.skillMenu.task.stop()
	_ = m.syncClientSkills()
	failed := clientSkillsResultMsg{generation: m.skillMenu.task.gen, epoch: m.conversationEpoch, err: context.DeadlineExceeded}
	_ = m.applyClientSkills(failed)
	if !strings.Contains(latestNotice(m).Text, "Could not load client skill suggestions") || m.clientSkills != nil {
		t.Fatal("failure did not reach the user")
	}
	if m.syncClientSkills() != nil {
		t.Fatal("failed load retried within the same conversation")
	}
	m.conversationEpoch++
	cmd := m.syncClientSkills()
	if cmd == nil {
		t.Fatal("new conversation did not reload the menu")
	}
	m.applyClientSkills(cmd().(clientSkillsResultMsg))
	if m.clientSkills == nil || len(m.clientSkills) != 0 {
		t.Fatal("empty successful registry was not accepted")
	}
}

func TestClientSkillUnknownNameKeepsDraftOnceMenuLoaded(t *testing.T) {
	m := newModelWithSpy(t)
	m.applyClientSkills(clientSkillsResultMsg{skills: []agent.SkillSummary{{Name: "review"}}})
	m.editor.SetValue("/skill:missing Keep CASE")
	_, _ = m.submit()
	if m.op.events != nil || m.editor.Value() != "/skill:missing Keep CASE" {
		t.Fatal("unknown skill started a turn or cleared the draft")
	}
	if !strings.Contains(latestNotice(m).Text, "Unknown client skill: missing") {
		t.Fatal("unknown skill was not reported")
	}
}

func TestClientSkillReadFailureFailsTurnLikeBackendError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	path := installTestSkill(t, root, "review", true)
	m := New(agent.New(&spyBackend{t: t}, assistant.SendOptions{}, agent.WithClientSkills(agent.NewClientSkills(root, nil))))
	m.applyClientSkills(m.loadClientSkillMenu()().(clientSkillsResultMsg))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m.editor.SetValue("/skill:review argument")
	_, _ = m.submit()
	var failure error
	for event := range m.op.events {
		if event.Err != nil {
			failure = event.Err
		}
		m.applyEvent(event)
	}
	if failure == nil || m.chatPhase != chat.PhaseError {
		t.Fatalf("failure = %v, phase = %v", failure, m.chatPhase)
	}
	if prompts := m.transcript.UserPrompts(); len(prompts) != 1 || m.editor.Value() != "" {
		t.Fatal("failed skill turn did not keep its user block")
	}
}

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func writeSkill(t *testing.T, root, path, name, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, root, filepath.Join(path, "SKILL.md"), fmt.Sprintf("---\nname: %s\ndescription: %s\n---\nBODY MUST NOT APPEAR IN CATALOG", name, description))
}

func writeSkillFile(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstructionTestFile(t, root, filepath.Join(path, "SKILL.md"), content)
}

func TestClientSkillConversationSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "original description")
	backend := &instructionDeliveryBackend{}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(env.NewSkills(dir, nil)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "first"}))
	writeSkill(t, dir, ".agents/skills/example", "example", "changed description")
	for range 2 {
		drain(engine.StartTurn(context.Background(), TurnInput{Message: "retry or continue"}))
	}
	if !strings.Contains(backend.contexts[0], "original description") || backend.contexts[0] != backend.contexts[1] || backend.contexts[1] != backend.contexts[2] {
		t.Fatalf("catalog not retained: %q", backend.contexts)
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "new"}))
	if !strings.Contains(backend.contexts[3], "changed description") {
		t.Fatal("new conversation did not rediscover skills")
	}
}

func TestClientSkillsOnlySentWithUserMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	backend := &contextRecordingBackend{}
	tools, err := NewToolSet(ModeSkipPermissions, Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(env.NewSkills(dir, nil)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "inspect", Tools: tools, UserContext: func(context.Context) string { return "environment" }}))
	if len(backend.userContexts) != 2 || !strings.HasPrefix(backend.userContexts[0], "environment\n\n") || !strings.Contains(backend.userContexts[0], "example description") || backend.userContexts[1] != "" {
		t.Fatalf("contexts = %q", backend.userContexts)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "next", Tools: tools, UserContext: func(context.Context) string { return "environment" }}))
	if !strings.Contains(backend.userContexts[2], "example description") || strings.Contains(backend.userContexts[2], "environment") {
		t.Fatal("catalog missing or environment context repeated")
	}
}

func TestClientSkillsResumeAndEmptySnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	engine := New(nil, assistant.SendOptions{}, WithClientSkills(env.NewSkills(dir, nil)))
	opts := engine.applySkills(context.Background(), assistant.SendOptions{}, "")
	if opts.CustomUserContext != "" {
		t.Fatal("unexpected empty catalog")
	}
	writeSkill(t, dir, ".agents/skills/example", "example", "created later")
	if got := engine.applySkills(context.Background(), opts, ""); got.CustomUserContext != "" {
		t.Fatal("empty snapshot was rediscovered")
	}
	engine.resetContext()
	if got := engine.applySkills(context.Background(), assistant.SendOptions{ConversationID: "existing"}, ""); !strings.Contains(got.CustomUserContext, "created later") {
		t.Fatalf("resumed conversation did not rescan skills: %q", got.CustomUserContext)
	}
	engine.resetContext()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine.applySkills(ctx, assistant.SendOptions{}, "")

	if got := engine.applySkills(context.Background(), assistant.SendOptions{}, ""); !strings.Contains(got.CustomUserContext, "created later") {
		t.Fatal("reset did not discover skills")
	}
}

func TestClientSkillsResumeWithoutSkillsSupersedesOlderCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	engine := New(nil, assistant.SendOptions{}, WithClientSkills(env.NewSkills(t.TempDir(), nil)))
	for range 2 {
		got := engine.applySkills(context.Background(), assistant.SendOptions{ConversationID: "existing"}, "")
		if got.CustomUserContext != clientSkillsRemovalNotice {
			t.Fatalf("resume without skills = %q", got.CustomUserContext)
		}
	}
	engine.resetContext()
	if got := engine.applySkills(context.Background(), assistant.SendOptions{}, ""); got.CustomUserContext != "" {
		t.Fatalf("new conversation without skills = %q", got.CustomUserContext)
	}
}

func TestClientSkillsSwitchAndStoppedToolDrain(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	backend := &instructionDeliveryBackend{}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(env.NewSkills(dir, nil)))
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "first"}))
	if _, _, err := engine.drainStoppedToolCalls(context.Background(), nil, assistant.SendOptions{ConversationID: engine.ConversationID()}); err != nil {
		t.Fatal(err)
	}
	if backend.contexts[1] != "" {
		t.Fatal("stopped tool drain included catalog")
	}
	if err := engine.InstallConversation(context.Background(), &Conversation{id: "existing", transcript: NewTranscript()}); err != nil {
		t.Fatal(err)
	}
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "resumed"}))
	if !strings.Contains(backend.contexts[2], "example description") {
		t.Fatalf("restored conversation did not rescan skills: %q", backend.contexts[2])
	}
}

func TestClientSkillsDeferredUntilUserMessageAfterToolResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/example", "example", "example description")
	tools, err := NewToolSet(ModeSkipPermissions, Tool{
		Definition: assistant.ClientTool{Name: "input"}, Resumable: true,
		Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &optionsBackend{}
	skills := env.NewSkills(dir, nil)
	engine := New(backend, assistant.SendOptions{ConversationID: "conversation"}, WithClientSkills(skills))
	engine.continuation = continuationFromHistory([]assistant.Message{savedCall("one", "input")})
	drain(engine.ResumePendingTools(t.Context(), TurnInput{Tools: tools}))
	if backend.opts.CustomUserContext != "" {
		t.Fatal("resumed client tool response prepared or sent the catalog")
	}
	writeSkill(t, dir, ".agents/skills/example", "example", "updated after tool resume")
	drain(engine.StartTurn(t.Context(), TurnInput{Message: "continue", Tools: tools}))
	if !strings.Contains(backend.opts.CustomUserContext, "updated after tool resume") {
		t.Fatal("next user message did not include the catalog")
	}
}

func TestSkillRegistrySharedByMenuAndCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/review", "review", "original description")
	writeSkillFile(t, dir, ".agents/skills/manual", "---\nname: manual\ndescription: User only\nmodel-invocable: false\n---\n")
	backend := &instructionDeliveryBackend{}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(env.NewSkills(dir, nil)))
	menu := mustClientSkills(t, engine)
	if len(menu) != 2 || menu[0].Name != "manual" {
		t.Fatalf("menu = %+v", menu)
	}
	menu[1].Description = "mutated by consumer"
	writeSkill(t, dir, ".agents/skills/review", "review", "changed description")
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "first"}))
	if !strings.Contains(backend.contexts[0], "original description") || strings.Contains(backend.contexts[0], "User only") {
		t.Fatalf("catalog differs from menu: %q", backend.contexts[0])
	}
	if got := mustClientSkills(t, engine)[1].Description; got != "original description" {
		t.Fatalf("registry mutated: %s", got)
	}
	if err := engine.NewConversation(); err != nil {
		t.Fatal(err)
	}
	// Headless turns can populate the same cache before any menu requests it.
	drain(engine.StartTurn(context.Background(), TurnInput{Message: "new"}))
	writeSkill(t, dir, ".agents/skills/review", "review", "resumed description")
	if got := mustClientSkills(t, engine)[1].Description; got != "changed description" {
		t.Fatalf("menu rescanned engine registry: %s", got)
	}
	if err := engine.InstallConversation(context.Background(), &Conversation{id: "existing", transcript: NewTranscript()}); err != nil {
		t.Fatal(err)
	}
	if got := mustClientSkills(t, engine)[1].Description; got != "resumed description" {
		t.Fatalf("resume retained old registry: %s", got)
	}
}

func mustClientSkills(t *testing.T, engine *Engine) []SkillSummary {
	t.Helper()
	skills, err := engine.ClientSkills(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return skills
}

func TestClientSkillsReturnsDiscoveryError(t *testing.T) {
	engine := New(nil, assistant.SendOptions{}, WithClientSkills(env.NewSkills(t.TempDir(), nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summaries, err := engine.ClientSkills(ctx)
	if !errors.Is(err, context.Canceled) || summaries != nil {
		t.Fatalf("canceled load = (%v, %v)", summaries, err)
	}
}

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestSkillRegistrySharedByMenuAndCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeSkill(t, dir, ".agents/skills/review", "review", "original description")
	writeSkillFile(t, dir, ".agents/skills/manual", "---\nname: manual\ndescription: User only\nmodel-invocable: false\n---\n")
	backend := &instructionDeliveryBackend{}
	engine := New(backend, assistant.SendOptions{}, WithClientSkills(NewClientSkills(dir, nil)))
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
	engine := New(nil, assistant.SendOptions{}, WithClientSkills(NewClientSkills(t.TempDir(), nil)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summaries, err := engine.ClientSkills(ctx)
	if !errors.Is(err, context.Canceled) || summaries != nil {
		t.Fatalf("canceled load = (%v, %v)", summaries, err)
	}
}

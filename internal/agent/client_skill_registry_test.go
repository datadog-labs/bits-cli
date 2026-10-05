package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
)

func TestSkillRegistrySharesDiscoveryAndCallerCancellation(t *testing.T) {
	var cache skillRegistryCache
	defer cache.reset()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	var scans atomic.Int32
	discover := func(ctx context.Context) []registeredSkill {
		if scans.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return []registeredSkill{{Name: "review", Description: "Review"}}
	}
	firstCtx, stopFirst := context.WithCancel(ctx)
	first := make(chan error, 1)
	go func() { _, err := cache.load(firstCtx, discover); first <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second := make(chan *skillRegistry, 1)
	go func() { registry, _ := cache.load(ctx, discover); second <- registry }()
	stopFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter = %v", err)
	}
	close(release)
	registry := <-second
	if registry == nil || registry.err != nil || len(registry.skills) != 1 || scans.Load() != 1 {
		t.Fatalf("registry = %+v, scans = %d", registry, scans.Load())
	}
	again, err := cache.load(ctx, discover)
	if err != nil || again != registry || scans.Load() != 1 {
		t.Fatal("completed registry was rescanned")
	}
}

func TestSkillRegistryResetDuringDiscovery(t *testing.T) {
	var cache skillRegistryCache
	defer cache.reset()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	finishOld := make(chan struct{})
	oldDone := make(chan error, 1)
	go func() {
		_, err := cache.load(ctx, func(scanCtx context.Context) []registeredSkill {
			close(started)
			<-scanCtx.Done()
			<-finishOld
			return []registeredSkill{{Name: "old"}}
		})
		oldDone <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cache.reset()
	fresh, err := cache.load(ctx, func(context.Context) []registeredSkill { return []registeredSkill{{Name: "new"}} })
	if err != nil {
		t.Fatal(err)
	}
	close(finishOld)
	if err := <-oldDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("old discovery = %v", err)
	}
	again, err := cache.load(ctx, func(context.Context) []registeredSkill { t.Error("old completion evicted new registry"); return nil })
	if err != nil || again != fresh || again.skills[0].Name != "new" {
		t.Fatal("reset lost the new registry")
	}
}

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

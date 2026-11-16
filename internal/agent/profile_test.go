package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestNewAppliesDefaultSkillOverrides(t *testing.T) {
	engine := New(nil, Config{})
	for _, name := range []string{
		"canvas-builder", "code-investigation", "code-sandbox", "code-search",
		"coding", "dashboard-builder", "exploring-commit-history",
		"generate_ddsql_query", "update-cloudcraft-diagram",
	} {
		source := assistant.SkillSourceAssistant
		if name == "code-search" {
			source = assistant.SkillSourceDatadogMCP
		}
		want := assistant.SkillOverride{Name: name, Source: source, Enabled: false}
		if !slices.Contains(engine.opts.SkillOverrides, want) {
			t.Errorf("missing default override: %+v", want)
		}
	}
}

func TestNewPreservesExplicitSkillOverrides(t *testing.T) {
	explicit := []assistant.SkillOverride{
		{Name: "coding", Source: assistant.SkillSourceAssistant, Enabled: true},
		{Name: "code-search", Source: assistant.SkillSourceAssistant, Enabled: true},
		{Name: "custom", Source: assistant.SkillSourceAssistant, Enabled: false},
	}
	// Spare capacity exposes accidental appends into the caller's backing array.
	backing := make([]assistant.SkillOverride, 20)
	copy(backing, explicit)
	before := slices.Clone(backing)
	engine := New(nil, Config{SkillOverrides: backing[:len(explicit)]})
	if !slices.Equal(backing, before) {
		t.Fatal("construction mutated caller-owned overrides")
	}
	for _, want := range append(slices.Clone(explicit), assistant.SkillOverride{
		Name: "code-search", Source: assistant.SkillSourceDatadogMCP, Enabled: false,
	}) {
		count := 0
		for _, got := range engine.opts.SkillOverrides {
			if got.Name == want.Name && got.Source == want.Source {
				count++
				if got.Enabled != want.Enabled {
					t.Errorf("override = %+v, want %+v", got, want)
				}
			}
		}
		if count != 1 {
			t.Errorf("override %+v appears %d times, want once", want, count)
		}
	}
	engine.opts.SkillOverrides[0].Enabled = false
	if !slices.Equal(backing, before) {
		t.Fatal("engine overrides alias caller-owned overrides")
	}
	other := New(nil, Config{})
	for _, override := range other.opts.SkillOverrides {
		if override.Enabled {
			t.Fatalf("explicit override changed defaults for another engine: %+v", override)
		}
	}
}

func TestTurnSkillOverridesApplyToOneTurn(t *testing.T) {
	backend := &scriptBackend{rounds: toolRounds("read"), convID: "conversation-1"}
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "ok"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	engine := New(backend, Config{PermissionsMode: ModeSkipPermissions})
	library := assistant.SkillOverride{Name: "skills-library/clear-writing", Source: assistant.SkillSourceSkillsLibrary, Enabled: true}
	coding := assistant.SkillOverride{Name: "coding", Source: assistant.SkillSourceAssistant, Enabled: true}
	drain(t, engine, TurnInput{Message: "first", Tools: tools, SkillOverrides: []assistant.SkillOverride{library, coding}})
	drain(t, engine, TurnInput{Message: "second", Tools: tools})
	if len(backend.opts) != 3 {
		t.Fatalf("requests = %d, want 3", len(backend.opts))
	}
	for i, opts := range backend.opts {
		turn := i < 2 // the first turn includes its tool-result continuation
		if got := slices.Contains(opts.SkillOverrides, library); got != turn {
			t.Errorf("request %d library override = %v, want %v", i, got, turn)
		}
		if got := slices.Contains(opts.SkillOverrides, coding); got != turn {
			t.Errorf("request %d coding enabled = %v, want %v", i, got, turn)
		}
		if turn && slices.Contains(opts.SkillOverrides, assistant.SkillOverride{Name: "coding", Source: assistant.SkillSourceAssistant}) {
			t.Errorf("request %d kept the replaced default", i)
		}
	}
}

func TestServerSkills(t *testing.T) {
	if _, err := New(&scriptBackend{}, Config{}).ServerSkills(t.Context()); !errors.Is(err, ErrSkillsUnsupported) {
		t.Fatalf("err = %v, want ErrSkillsUnsupported", err)
	}
	want := []assistant.Skill{{Name: "skills-library/clear-writing", Source: "skills_library"}}
	skills, err := New(&skillsBackend{skills: want}, Config{}).ServerSkills(t.Context())
	if err != nil || !slices.Equal(skills, want) {
		t.Fatalf("skills = %v, err = %v", skills, err)
	}
}

type skillsBackend struct {
	scriptBackend
	skills []assistant.Skill
}

func (b *skillsBackend) ListSkills(context.Context) ([]assistant.Skill, error) { return b.skills, nil }

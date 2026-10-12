package agent

import (
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

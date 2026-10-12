package agent

import "github.com/datadog-labs/bits-cli/internal/assistant"

// defaultSkillOverrides keep Bits on its local-workspace tool path rather than
// activating the remote code skills, and disable skills that depend on active
// web browser pages or client-side UI tools (such as Canvas or the DDSQL editor).
// They are sent with every Assistant request, including client-tool
// continuations, by the engine. This policy could move to the backend CLI
// profile once it is appropriate for every CLI client, but stays here for now
// so the harness enforces it independently.
var defaultSkillOverrides = []assistant.SkillOverride{
	{Name: "canvas-builder", Source: assistant.SkillSourceAssistant, Enabled: false},            // Requires an open browser Canvas and its client tools.
	{Name: "code-investigation", Source: assistant.SkillSourceAssistant, Enabled: false},        // Uses the remote Code Sandbox instead of the local workspace.
	{Name: "code-sandbox", Source: assistant.SkillSourceAssistant, Enabled: false},              // Provides remote sandbox file and execution tools unavailable in Bits.
	{Name: "code-search", Source: assistant.SkillSourceDatadogMCP, Enabled: false},              // Keeps repository investigation on Bits' local workspace tools.
	{Name: "coding", Source: assistant.SkillSourceAssistant, Enabled: false},                    // Starts remote coding sessions rather than editing locally.
	{Name: "dashboard-builder", Source: assistant.SkillSourceAssistant, Enabled: false},         // Requires an open dashboard page and browser client tools.
	{Name: "exploring-commit-history", Source: assistant.SkillSourceAssistant, Enabled: false},  // Uses remote code-search history instead of the local Git checkout.
	{Name: "generate_ddsql_query", Source: assistant.SkillSourceAssistant, Enabled: false},      // Targets the browser DDSQL editor and its mutation tools.
	{Name: "update-cloudcraft-diagram", Source: assistant.SkillSourceAssistant, Enabled: false}, // Requires the currently open Cloudcraft diagram page.
}

// mergeSkillOverrides adds defaults for skills the caller has not configured.
// Explicit entries take precedence by name and source; the input is not mutated.
func mergeSkillOverrides(explicit []assistant.SkillOverride) []assistant.SkillOverride {
	overrides := append([]assistant.SkillOverride(nil), explicit...)
	configured := make(map[skillKey]struct{}, len(overrides))
	for _, override := range overrides {
		configured[skillKey{name: override.Name, source: override.Source}] = struct{}{}
	}
	for _, override := range defaultSkillOverrides {
		key := skillKey{name: override.Name, source: override.Source}
		if _, ok := configured[key]; ok {
			continue
		}
		overrides = append(overrides, override)
	}
	return overrides
}

type skillKey struct {
	name   string
	source assistant.SkillSource
}

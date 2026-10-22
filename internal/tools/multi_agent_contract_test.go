package tools

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// These limits belong to the multi-agent run API. Tool definitions are fixed
// by the CLI, so verify the shipped sets here instead of inspecting each turn.
func TestClientToolsFitMultiAgentContract(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	for _, interactive := range []bool{false, true} {
		name := "headless"
		if interactive {
			name = "interactive"
		}
		t.Run(name, func(t *testing.T) {
			tools := NewClientTools(ws)
			if interactive {
				tools = append(tools, NewAskUserQuestionTool(nil))
			}
			set, err := agent.NewToolSet(tools...)
			if err != nil {
				t.Fatal(err)
			}
			definitions := set.Definitions()
			if len(definitions) > 64 {
				t.Fatalf("tool count = %d, limit 64", len(definitions))
			}
			total := 0
			for _, tool := range definitions {
				t.Run(tool.Name, func(t *testing.T) {
					if utf8.RuneCountInString(tool.Name) > 128 {
						t.Error("name exceeds 128 characters")
					}
					if utf8.RuneCountInString(tool.Description) > 4096 {
						t.Error("description exceeds 4096 characters")
					}
					schema, err := json.Marshal(tool.InputSchema)
					if err != nil {
						t.Fatal(err)
					}
					total += len(schema)
					if len(schema) > 64_000 {
						t.Errorf("schema bytes = %d, limit 64000", len(schema))
					}
					var value any
					if err := json.Unmarshal(schema, &value); err != nil {
						t.Fatal(err)
					}
					if !multiAgentSchemaDepthAllowed(value, 0) {
						t.Error("schema exceeds 16 levels of nesting")
					}
				})
			}
			if total > 512_000 {
				t.Errorf("total schema bytes = %d, limit 512000", total)
			}
		})
	}
}

func multiAgentSchemaDepthAllowed(value any, depth int) bool {
	switch v := value.(type) {
	case map[string]any:
		if depth > 16 {
			return false
		}
		for _, child := range v {
			if !multiAgentSchemaDepthAllowed(child, depth+1) {
				return false
			}
		}
	case []any:
		if depth > 16 {
			return false
		}
		for _, child := range v {
			if !multiAgentSchemaDepthAllowed(child, depth+1) {
				return false
			}
		}
	}
	return true
}

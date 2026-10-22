package tools

import (
	"context"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

const handoffAccepted = "The user is now in Bits CLI on their machine"

// NewHandoffTool answers the bits_cli_handoff call Bits web leaves pending.
func NewHandoffTool() agent.Tool {
	return agent.Tool{
		Resumable:       true,
		ContextOnResume: true,
		Hidden:          true,
		Definition: assistant.ClientTool{
			Name:        spec.Handoff,
			Description: "Used only by Bits web to hand a conversation to Bits CLI. Never call it from Bits CLI.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{"type": "string"},
				},
			},
		},
		Handler: func(_ context.Context, call agent.ToolCall) (agent.ToolResult, error) {
			return handoff(call), nil
		},
	}
}

func handoff(agent.ToolCall) agent.ToolResult {
	return agent.ToolResult{Title: "Handed off to Bits CLI", Output: handoffAccepted, Display: "Continuing in Bits CLI"}
}

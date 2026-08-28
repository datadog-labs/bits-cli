package localtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
)

const Name = "get_local_time"

func New(now func() time.Time) agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:             Name,
			Description:      "Get the current time and time zone from this local Bits CLI process.",
			RequiresApproval: assistant.ToolApprovalYes,
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		Approval: func(agent.ToolCall) (agent.ApprovalRequirement, bool) {
			return agent.ApprovalRequirement{
				Key: agent.ApprovalKey{Tool: Name, Resource: "process-clock"},
				Prompt: agent.ApprovalPrompt{
					Title:  "Share your local time?",
					Detail: "Bits will read this process's clock and time-zone configuration.",
				},
			}, true
		},
		Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
			current := now()
			zone, offset := current.Zone()
			output, err := json.Marshal(struct {
				Timestamp string `json:"timestamp"`
				Timezone  string `json:"timezone"`
				UTCOffset string `json:"utc_offset"`
			}{
				Timestamp: current.Format(time.RFC3339Nano),
				Timezone:  zone,
				UTCOffset: utcOffset(offset),
			})
			if err != nil {
				return agent.ToolResult{}, err
			}
			return agent.ToolResult{Title: "Local time", Output: string(output)}, nil
		},
	}
}

func utcOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

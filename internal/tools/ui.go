package tools

import (
	"context"

	"github.com/DataDog/bits-cli/internal/agent"
)

// Interactor lets a tool hand its call to the user and wait for the result.
type Interactor interface {
	Interact(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error)
}

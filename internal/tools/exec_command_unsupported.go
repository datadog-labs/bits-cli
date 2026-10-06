//go:build !linux && !darwin

package tools

import (
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

func platformExecTools(*workspace.Workspace) []agent.Tool { return nil }

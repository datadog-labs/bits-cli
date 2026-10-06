//go:build !linux && !darwin

package tools

import (
	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/workspace"
)

func platformExecTools(*workspace.Workspace) []agent.Tool { return nil }

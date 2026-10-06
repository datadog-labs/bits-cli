//go:build linux || darwin

package tools

import (
	"github.com/datadog-labs/bits-cli/internal/agent"
	exectool "github.com/datadog-labs/bits-cli/internal/tools/exec"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

func platformExecTools(ws *workspace.Workspace) []agent.Tool {
	return []agent.Tool{newExecCommandTool(ws.Path(), exectool.NewExecService(ws.DefaultShellPath()))}
}

//go:build linux || darwin

package tools

import (
	"github.com/DataDog/bits-cli/internal/agent"
	exectool "github.com/DataDog/bits-cli/internal/tools/exec"
	"github.com/DataDog/bits-cli/internal/workspace"
)

func platformExecTools(ws *workspace.Workspace) []agent.Tool {
	return []agent.Tool{newExecCommandTool(ws.Path(), exectool.NewExecService(ws.DefaultShellPath()))}
}

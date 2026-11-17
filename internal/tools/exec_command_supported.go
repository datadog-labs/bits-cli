//go:build linux || darwin || windows

package tools

import (
	"github.com/datadog-labs/bits-cli/internal/agent"
	exectool "github.com/datadog-labs/bits-cli/internal/tools/exec"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

func platformExecTools(ws *workspace.Workspace, commands *exectool.ExecService) []agent.Tool {
	return []agent.Tool{
		newExecCommandTool(ws.Path(), commands),
		newWaitCommandTool(commands),
		newStopCommandTool(commands),
	}
}

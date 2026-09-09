//go:build linux || darwin

package tools

import (
	"github.com/DataDog/bits-cli/internal/agent"
	exectool "github.com/DataDog/bits-cli/internal/tools/exec"
)

func platformExecTools(turnCWD string) []agent.Tool {
	return []agent.Tool{newExecCommandTool(turnCWD, exectool.NewExecService())}
}

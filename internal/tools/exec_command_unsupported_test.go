//go:build !linux && !darwin

package tools

import (
	"testing"

	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

func TestNewClientToolsDoesNotAdvertiseExecCommandOnUnsupportedPlatforms(t *testing.T) {
	workspace, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	clientTools := NewClientTools(workspace)
	for _, tool := range clientTools {
		if tool.Definition.Name == spec.ExecCommand {
			t.Fatal("exec_command was advertised on an unsupported platform")
		}
	}
}

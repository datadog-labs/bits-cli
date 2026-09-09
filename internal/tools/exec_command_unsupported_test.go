//go:build !linux && !darwin

package tools

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/tools/spec"
)

func TestNewClientToolsDoesNotAdvertiseExecCommandOnUnsupportedPlatforms(t *testing.T) {
	clientTools, err := NewClientTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range clientTools {
		if tool.Definition.Name == spec.ExecCommand {
			t.Fatal("exec_command was advertised on an unsupported platform")
		}
	}
}

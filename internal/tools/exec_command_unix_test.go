//go:build linux || darwin

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	exectool "github.com/DataDog/bits-cli/internal/tools/exec"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/workspace"
)

func TestNewClientToolsAdvertisesExecCommandOnUnix(t *testing.T) {
	workspace, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	clientTools := NewClientTools(workspace)
	var found int
	for _, tool := range clientTools {
		if tool.Definition.Name == spec.ExecCommand {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("exec_command definitions = %d, want 1", found)
	}
}

func TestExecCommandUnixEndToEndWorkdirsAndFailures(t *testing.T) {
	turnCWD := t.TempDir()
	child := filepath.Join(turnCWD, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(turnCWD, "file")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(turnCWD)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	tool := newExecCommandTool(turnCWD, exectool.NewExecService(ws.DefaultShellPath()))
	tests := []struct {
		name       string
		input      string
		wantStatus exectool.ExecTerminalReason
		wantStdout string
	}{
		{name: "default", input: `{"cmd":"pwd"}`, wantStatus: exectool.ExecSucceeded, wantStdout: turnCWD},
		{name: "relative", input: `{"cmd":"pwd","workdir":"child"}`, wantStatus: exectool.ExecSucceeded, wantStdout: child},
		{name: "absolute", input: `{"cmd":"pwd","workdir":` + mustJSONString(t, child) + `}`, wantStatus: exectool.ExecSucceeded, wantStdout: child},
		{name: "missing", input: `{"cmd":"pwd","workdir":"missing"}`, wantStatus: exectool.ExecLaunchFailed},
		{name: "not directory", input: `{"cmd":"pwd","workdir":` + mustJSONString(t, regular) + `}`, wantStatus: exectool.ExecLaunchFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := tool.Handler(context.Background(), agent.ToolCall{Input: test.input})
			if err != nil {
				t.Fatal(err)
			}
			var model spec.ExecCommandOutput
			if err := json.Unmarshal([]byte(result.Output), &model); err != nil {
				t.Fatalf("result is not JSON: %v\n%s", err, result.Output)
			}
			if model.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q (%+v)", model.Status, test.wantStatus, model)
			}
			if test.wantStdout != "" && strings.TrimSpace(model.Stdout) != test.wantStdout {
				t.Fatalf("stdout = %q, want %q", model.Stdout, test.wantStdout)
			}
		})
	}
}

package tools

import (
	"encoding/json"
	"fmt"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/workspace"
)

// approvalKeyWorkspaceRead is the shared approval key used by the read-only
// editor tools, so a single allow-session decision covers read_file,
// list_files, and grep_files for the lifetime of the session.
const approvalKeyWorkspaceRead = "workspace_read"

// approvalKeyWorkspaceWrite is the shared approval key used by the mutating
// editor tools, so a single allow-session decision covers write_file and
// edit_file across the whole workspace for the lifetime of the session.
const approvalKeyWorkspaceWrite = "workspace_write"

// NewEditorTools returns the editor tools for workspace: the read-only
// read_file, list_files, and grep_files, plus the mutating write_file and
// edit_file.
//
// The three read-only tools share one approval key; the two mutating tools
// share another. A single allow-session decision on either key covers all
// tools in that group for the workspace. Mutations to the same path are
// serialized through a shared locker.
func NewEditorTools(ws *workspace.Workspace) []agent.Tool {
	root := ws.Path()
	r := ws.OSRoot()
	fsys := ws.FS()
	locker := newMutationLocker()
	return []agent.Tool{
		newReadFileTool(fsys, root),
		newListFilesTool(fsys, root),
		newGrepFilesTool(fsys, root),
		newWriteFileTool(r, root, locker),
		newEditFileTool(r, root, locker),
	}
}

// NewClientTools returns every local tool available on the current platform.
// The editor tools are available everywhere. Linux and macOS additionally get
// the deliberately unsandboxed, one-shot exec_command tool.
func NewClientTools(ws *workspace.Workspace) []agent.Tool {
	clientTools := NewEditorTools(ws)
	return append(clientTools, platformExecTools(ws.Path())...)
}

// errorResult builds an error ToolResult with a formatted output message.
func errorResult(format string, args ...any) agent.ToolResult {
	return agent.ToolResult{IsError: true, Output: fmt.Sprintf(format, args...)}
}

// workspaceReadApproval returns the shared approval policy for the read-only editor tools.
func workspaceReadApproval(root string) agent.ApprovalPolicy {
	return func(call agent.ToolCall) (agent.ApprovalRequirement, bool) {
		return agent.ApprovalRequirement{
			Key: agent.ApprovalKey{Tool: approvalKeyWorkspaceRead, Resource: root},
			Prompt: agent.ApprovalPrompt{
				Title:  workspaceApprovalTitle(call, "Share workspace files?"),
				Detail: "Bits will read files in " + root + " and share the requested content with the assistant",
			},
		}, true
	}
}

// workspaceWriteApproval returns the shared approval policy for the mutating
// editor tools. It gates the whole workspace, not a single path, so one
// allow-session decision authorizes every write_file and edit_file for the
// session.
func workspaceWriteApproval(root string) agent.ApprovalPolicy {
	return func(call agent.ToolCall) (agent.ApprovalRequirement, bool) {
		return agent.ApprovalRequirement{
			Key: agent.ApprovalKey{Tool: approvalKeyWorkspaceWrite, Resource: root},
			Prompt: agent.ApprovalPrompt{
				Title:  workspaceApprovalTitle(call, "Modify workspace files?"),
				Detail: "Bits will create, overwrite, and edit files in " + root,
			},
		}, true
	}
}

// Name the requested target without changing the workspace-wide session grant.
// Handlers still validate the input; malformed requests retain a generic prompt.
func workspaceApprovalTitle(call agent.ToolCall, fallback string) string {
	var args spec.PathInput
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return fallback
	}
	if args.Path == "" {
		switch call.Name {
		case spec.ListFiles, spec.GrepFiles:
			args.Path = "."
		default:
			return fallback
		}
	}
	switch call.Name {
	case spec.ReadFile:
		return fmt.Sprintf("Read %q?", args.Path)
	case spec.ListFiles:
		return fmt.Sprintf("List files in %q?", args.Path)
	case spec.GrepFiles:
		return fmt.Sprintf("Search files in %q?", args.Path)
	case spec.WriteFile:
		return fmt.Sprintf("Create or overwrite %q?", args.Path)
	case spec.EditFile:
		return fmt.Sprintf("Edit %q?", args.Path)
	default:
		return fallback
	}
}

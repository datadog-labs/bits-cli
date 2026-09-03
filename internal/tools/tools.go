package tools

import (
	"fmt"
	"os"

	"github.com/DataDog/bits-cli/internal/agent"
)

// Tool names exposed to the assistant.
const (
	toolReadFile  = "read_file"
	toolListFiles = "list_files"
	toolGrepFiles = "grep_files"
	toolWriteFile = "write_file"
	toolEditFile  = "edit_file"
)

// approvalKeyWorkspaceRead is the shared approval key used by the read-only
// editor tools, so a single allow-session decision covers read_file,
// list_files, and grep_files for the lifetime of the session.
const approvalKeyWorkspaceRead = "workspace_read"

// approvalKeyWorkspaceWrite is the shared approval key used by the mutating
// editor tools, so a single allow-session decision covers write_file and
// edit_file across the whole workspace for the lifetime of the session.
const approvalKeyWorkspaceWrite = "workspace_write"

// NewEditorTools opens root as a confined workspace and returns the editor
// tools: the read-only read_file, list_files, and grep_files, plus the
// mutating write_file and edit_file.
//
// The three read-only tools share one approval key; the two mutating tools
// share another. A single allow-session decision on either key covers all
// tools in that group for the workspace. Mutations to the same path are
// serialized through a shared locker.
func NewEditorTools(root string) ([]agent.Tool, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open workspace %q: %w", root, err)
	}
	fsys := r.FS()
	locker := newMutationLocker()
	return []agent.Tool{
		newReadFileTool(fsys, root),
		newListFilesTool(fsys, root),
		newGrepFilesTool(fsys, root),
		newWriteFileTool(r, root, locker),
		newEditFileTool(r, root, locker),
	}, nil
}

// errorResult builds an error ToolResult with a formatted output message.
func errorResult(format string, args ...any) agent.ToolResult {
	return agent.ToolResult{IsError: true, Output: fmt.Sprintf(format, args...)}
}

// workspaceReadApproval returns the shared approval policy for the read-only editor tools.
func workspaceReadApproval(root string) agent.ApprovalPolicy {
	return func(_ agent.ToolCall) (agent.ApprovalRequirement, bool) {
		return agent.ApprovalRequirement{
			Key: agent.ApprovalKey{Tool: approvalKeyWorkspaceRead, Resource: root},
			Prompt: agent.ApprovalPrompt{
				Title:  "Share workspace files?",
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
	return func(_ agent.ToolCall) (agent.ApprovalRequirement, bool) {
		return agent.ApprovalRequirement{
			Key: agent.ApprovalKey{Tool: approvalKeyWorkspaceWrite, Resource: root},
			Prompt: agent.ApprovalPrompt{
				Title:  "Modify workspace files?",
				Detail: "Bits will create, overwrite, and edit files in " + root,
			},
		}, true
	}
}

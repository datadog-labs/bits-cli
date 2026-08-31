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
)

// approvalKeyWorkspaceRead is the shared approval key used by the read-only
// editor tools, so a single allow-session decision covers read_file,
// list_files, and grep_files for the lifetime of the session.
const approvalKeyWorkspaceRead = "workspace_read"

// NewEditorTools opens root as a confined workspace and returns the read-only
// editor tools: read_file, list_files, and grep_files.
// All three share one approval key; a single allow-session decision covers all of them.
func NewEditorTools(root string) ([]agent.Tool, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open workspace %q: %w", root, err)
	}
	fsys := r.FS()
	return []agent.Tool{
		newReadFileTool(fsys, root),
		newListFilesTool(fsys, root),
		newGrepFilesTool(fsys, root),
	}, nil
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

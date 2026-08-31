package tools

import "github.com/DataDog/bits-cli/internal/agent"

// approvalFor returns the shared approval policy for the workspace read tools.
// All three tools (read_file, list_files, grep_files) use the same key so a
// single allow-session decision covers all of them for the lifetime of the session.
func approvalFor(root string) agent.ApprovalPolicy {
	return func(_ agent.ToolCall) (agent.ApprovalRequirement, bool) {
		return agent.ApprovalRequirement{
			Key: agent.ApprovalKey{Tool: "workspace_read", Resource: root},
			Prompt: agent.ApprovalPrompt{
				Title:  "Share workspace files?",
				Detail: "Bits will read files in " + root + " and share the requested content with the assistant",
			},
		}, true
	}
}

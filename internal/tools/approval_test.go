package tools

import (
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
)

func TestWorkspaceApprovalDisclosesSharing(t *testing.T) {
	requirement, ok := approvalFor("/workspace")(agent.ToolCall{})
	if !ok {
		t.Fatal("workspace reads must require approval")
	}
	if !strings.Contains(strings.ToLower(requirement.Prompt.Title+" "+requirement.Prompt.Detail), "share") {
		t.Fatalf("approval does not disclose sharing: %+v", requirement.Prompt)
	}
}

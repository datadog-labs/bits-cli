package tui

import (
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
)

// waitingApprovals counts the approvals waiting on the user.
func waitingApprovals(m *Model) int {
	count := 0
	for _, r := range m.requests {
		if r.ui == nil {
			count++
		}
	}
	return count
}

// dockedToolUI is the tool request whose UI is docked, or nil.
func dockedToolUI(m *Model) *tools.Request {
	if len(m.requests) == 0 {
		return nil
	}
	return m.requests[0].ui
}

// queuedToolUIs are the tool UIs waiting behind the docked request.
func queuedToolUIs(m *Model) []*request {
	var queued []*request
	for i, r := range m.requests {
		if i > 0 && r.ui != nil {
			queued = append(queued, r)
		}
	}
	return queued
}

func awaitingCall(id string) agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: id},
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Name: "write_file", Status: agent.ToolAwaitingApproval},
	}
}

// Every request docks in turn with a fresh prompt, the docked one is never
// preempted, and the next one docked is the earliest call in the transcript.
func TestRequestsDockOneAtATimeInTranscriptOrder(t *testing.T) {
	m := newShell()
	a, b, c := awaitingCall("a"), awaitingCall("b"), awaitingCall("c")
	m.transcript.Blocks = []agent.Block{a, b, c}

	m.syncRequests([]agent.Block{b})
	docked := m.prompt().(*approvalPrompt)
	docked.selected = 2
	m.syncRequests([]agent.Block{a, b, c})
	if m.prompt() != docked || docked.selected != 2 {
		t.Fatal("a later update replaced or reset the docked approval")
	}
	if got := docked.waiting(); got != 3 {
		t.Fatalf("waiting = %d, want 3", got)
	}

	m.syncRequests([]agent.Block{a, c})
	next := m.prompt().(*approvalPrompt)
	if next.block.ToolCallID() != "a" || next.selected != 0 {
		t.Fatalf("docked %q with selection %d, want a fresh prompt for the earliest call", next.block.ToolCallID(), next.selected)
	}
	m.syncRequests(nil)
	if m.prompt() != nil {
		t.Fatal("settled approvals stayed docked")
	}
}

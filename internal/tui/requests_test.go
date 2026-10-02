package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tools/spec"
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
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	a, b, c := awaitingCall("a"), awaitingCall("b"), awaitingCall("c")
	m.transcript.Blocks = []agent.Block{a, b, c}

	m.syncRequests([]agent.Block{b})
	docked := m.prompt().(*approvalPrompt)
	docked.selected = 2
	m.syncRequests([]agent.Block{a, b, c})
	if m.prompt() != docked || docked.selected != 2 {
		t.Fatal("a later update replaced or reset the docked approval")
	}
	m.relayout()
	if dock := ansi.Strip(m.frame.dockView); !strings.Contains(dock, "3 waiting") {
		t.Fatalf("the docked approval does not count every request:\n%s", dock)
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

// The resize hint hides an approval at every size its controls or its
// request would not show at: a long command never docks as the one-line form
// that Enter could still allow.
func TestApprovalIsHiddenWhenItCannotBeShownWhole(t *testing.T) {
	rows := make([]string, 60)
	for i := range rows {
		rows[i] = fmt.Sprintf("print(%d)", i)
	}
	input, err := json.Marshal(spec.ExecCommandInput{Cmd: strings.Join(rows, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	command := agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: "exec"},
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Name: spec.ExecCommand, Input: string(input), Status: agent.ToolAwaitingApproval, IsClientSide: true},
	}
	for height := minimumChatHeight; height <= 40; height++ {
		m := newShell()
		m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
		m.syncRequests([]agent.Block{command})
		m.relayout()
		if m.frame.tooSmall {
			continue
		}
		if dock := ansi.Strip(m.frame.dockView); !strings.Contains(dock, "Deny") || !strings.Contains(dock, "lines ") {
			t.Fatalf("height %d docks an approval without its actions or scroll:\n%s", height, dock)
		}
	}
}

// Once answered, an approval takes no more input while it waits for the
// transcript to settle it, and an answer that did not get through is retried.
func TestAnsweredApprovalTakesNoMoreInput(t *testing.T) {
	m := newShell()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	prompt := newApprovalPrompt(awaitingCall("a"))
	var sent []agent.ApprovalDecision
	m.ask(&request{callID: "a", prompt: prompt, respond: func(answer any) bool {
		sent = append(sent, answer.(agent.ApprovalDecision))
		return len(sent) > 1 // the first attempt does not get through
	}})

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(sent) != 1 || m.requests[0].answered {
		t.Fatalf("sent %v, answered %t after a failed attempt", sent, m.requests[0].answered)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // any update retries
	if len(sent) != 2 || !m.requests[0].answered {
		t.Fatalf("sent %v, answered %t after the retry", sent, m.requests[0].answered)
	}
	selected := prompt.selected
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(sent) != 2 || prompt.selected != selected {
		t.Fatalf("an answered approval took input: sent %v, selected %d", sent, prompt.selected)
	}
}

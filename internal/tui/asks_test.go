package tui

import (
	"encoding/json"
	"fmt"
	"slices"
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
	for _, a := range m.approvals {
		if a.waiting() {
			count++
		}
	}
	return count
}

// dockedToolUI is the tool request whose UI is docked, or nil.
func dockedToolUI(m *Model) *tools.Request {
	if t, ok := m.shown.(*toolAsk); ok {
		return t.ui
	}
	return nil
}

// queuedToolUIs are the tool UIs waiting behind the docked ask.
func queuedToolUIs(m *Model) []*toolAsk {
	return slices.DeleteFunc(slices.Clone(m.toolUIs), func(t *toolAsk) bool { return t == m.shown })
}

// questionRequest presents ask_user_question as the tool would, and returns
// the request the TUI receives. It lives as long as the test.
func questionRequest(t *testing.T, id string) *tools.Request {
	t.Helper()
	ui := tools.NewUI()
	go func() {
		_, _ = tools.Interact[spec.QuestionAnswers](t.Context(), ui, agent.ToolCall{ID: id, Name: spec.AskUserQuestion, Input: questionInput})
	}()
	return <-ui.Requests()
}

func awaitingCall(id string) agent.Block {
	return agent.Block{
		ID:   agent.BlockID{Scope: agent.ScopeTool, Key: id},
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Name: "write_file", Status: agent.ToolAwaitingApproval},
	}
}

// stubDecisions makes every approval record its decisions instead of calling
// the engine; accept says whether one gets through.
func stubDecisions(m *Model, accept func(sent int) bool) *[]agent.ApprovalDecision {
	var sent []agent.ApprovalDecision
	for _, a := range m.approvals {
		a.decide = func(_ string, decision agent.ApprovalDecision) bool {
			sent = append(sent, decision)
			return accept(len(sent))
		}
	}
	return &sent
}

// Each approval docks in turn with a fresh prompt, the docked one is never
// replaced, and the next to dock is the earliest call in the transcript.
func TestApprovalsDockOneAtATimeInTranscriptOrder(t *testing.T) {
	m := newShell()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	a, b, c := awaitingCall("a"), awaitingCall("b"), awaitingCall("c")
	m.transcript.Blocks = []agent.Block{a, b, c}

	m.syncApprovals([]agent.Block{b})
	docked := m.prompt().(*approvalPrompt)
	docked.selected = 2
	m.syncApprovals([]agent.Block{a, b, c})
	if m.prompt() != docked || docked.selected != 2 {
		t.Fatal("a later update replaced or reset the docked approval")
	}
	m.relayout()
	if dock := ansi.Strip(m.frame.promptView); !strings.Contains(dock, "3 waiting") {
		t.Fatalf("the docked approval does not count every ask:\n%s", dock)
	}

	m.syncApprovals([]agent.Block{a, c})
	next := m.prompt().(*approvalPrompt)
	if next.block.ToolCallID() != "a" || next.selected != 0 {
		t.Fatalf("docked %q with selection %d, want a fresh prompt for the earliest call", next.block.ToolCallID(), next.selected)
	}
	m.syncApprovals(nil)
	if m.prompt() != nil {
		t.Fatal("settled approvals stayed docked")
	}
}

// A question and an approval wait at once: the first to arrive keeps the dock
// until answered, then the other docks.
func TestQuestionAndApprovalTakeTurns(t *testing.T) {
	m := newShell()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.transcript.Blocks = []agent.Block{awaitingCall("approve"), {ID: agent.BlockID{Scope: agent.ScopeTool, Key: "question"}, Kind: assistant.KindToolCall}}

	m.openToolUI(questionRequest(t, "question"))
	m.syncApprovals([]agent.Block{awaitingCall("approve")})
	if dockedToolUI(m) == nil || waitingApprovals(m) != 1 {
		t.Fatal("the approval, earlier in the transcript, replaced the docked question")
	}
	sent := stubDecisions(m, func(int) bool { return true })

	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape}) // dismiss the question
	if dockedToolUI(m) != nil || m.prompt() == nil {
		t.Fatal("the approval did not dock once the question was answered")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(*sent) != 1 || m.prompt() != nil {
		t.Fatalf("sent %v; the dock is not empty after both were answered", *sent)
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
		m.syncApprovals([]agent.Block{command})
		m.relayout()
		if m.frame.tooSmall {
			continue
		}
		if dock := ansi.Strip(m.frame.promptView); !strings.Contains(dock, "Deny") || !strings.Contains(dock, "lines ") {
			t.Fatalf("height %d docks an approval without its actions or scroll:\n%s", height, dock)
		}
	}
}

// An answered approval leaves the dock at once, though the transcript still
// lists it until the engine settles it, and is not asked again; an answer
// that did not get through stays docked and is retried.
func TestAnsweredApprovalLeavesTheDock(t *testing.T) {
	m := newShell()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	pending := []agent.Block{awaitingCall("a")}
	m.syncApprovals(pending)
	sent := stubDecisions(m, func(sent int) bool { return sent > 1 }) // the first attempt fails

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(*sent) != 1 || m.prompt() == nil {
		t.Fatalf("sent %v; a decision that did not get through left the dock", *sent)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight}) // any update retries
	if len(*sent) != 2 || m.prompt() != nil {
		t.Fatalf("sent %v; the delivered approval stayed docked", *sent)
	}
	m.syncApprovals(pending) // the engine has not settled it yet
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(*sent) != 2 || m.prompt() != nil || m.focus() != focusEditor {
		t.Fatalf("sent %v; the answered approval was asked again", *sent)
	}
}

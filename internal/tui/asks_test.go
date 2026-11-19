package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools"
	exectool "github.com/datadog-labs/bits-cli/internal/tools/exec"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/workspace"
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
	m.state.Blocks = []agent.Block{a, b, c}

	m.syncApprovals([]agent.Block{b})
	docked := m.prompt().(*approvalPrompt)
	docked.choices.Selected = 2
	m.syncApprovals([]agent.Block{a, b, c})
	if m.prompt() != docked || docked.choices.Selected != 2 {
		t.Fatal("a later update replaced or reset the docked approval")
	}
	m.relayout()
	if dock := ansi.Strip(m.frame.promptView); !strings.Contains(dock, "3 waiting") {
		t.Fatalf("the docked approval does not count every ask:\n%s", dock)
	}

	m.syncApprovals([]agent.Block{a, c})
	next := m.prompt().(*approvalPrompt)
	if next.block.ToolCallID() != "a" || next.choices.Selected != 0 {
		t.Fatalf("docked %q with selection %d, want a fresh prompt for the earliest call", next.block.ToolCallID(), next.choices.Selected)
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
	m.state.Blocks = []agent.Block{awaitingCall("approve"), {ID: agent.BlockID{Scope: agent.ScopeTool, Key: "question"}, Kind: assistant.KindToolCall}}

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
	m.relayout()
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

// An approval drafts only over a draft present when it docks: one created
// beside a draft the user has cleared since opens on Allow.
func TestApprovalDraftsOverTheDraftItDocksOver(t *testing.T) {
	m := newShell()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.editor.SetValue("fix the")
	m.syncApprovals([]agent.Block{awaitingCall("a"), awaitingCall("b"), awaitingCall("c")})
	sent := stubDecisions(m, func(int) bool { return true })
	if m.focus() != focusEditor {
		t.Fatal("the approval docked over a draft took the keyboard")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape}) // denies a while drafting
	if m.focus() != focusEditor {
		t.Fatal("the next approval took the keyboard from the draft")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // answers b from the panel
	if m.focus() != focusPrompt {
		t.Fatal("the next approval gave the keyboard back to the draft the user left")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // answers c
	want := []agent.ApprovalDecision{agent.ApprovalDeny, agent.ApprovalAllowOnce, agent.ApprovalAllowOnce}
	if !slices.Equal(*sent, want) || m.editor.Value() != "fix the" {
		t.Fatalf("sent %v with %q in the composer, want %v over the draft", *sent, m.editor.Value(), want)
	}
}

// A question takes the keyboard from a draft, so it closes the draft's
// completion menu.
func TestQuestionClosesTheCompletionMenu(t *testing.T) {
	m := newShell()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	for _, r := range "/sta" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !m.editor.MenuOpen() {
		t.Fatal("setup: typing a command did not open the menu")
	}
	m.openToolUI(questionRequest(t, "question"))
	if dockedToolUI(m) == nil || m.editor.MenuOpen() {
		t.Fatal("the docked question left the completion menu open")
	}
}

// A call the conversation still waits on after its operation ended, as one
// a restore found unanswered or a stop left running, keeps an approval
// panel, and deciding it releases the prompts it held.
func TestApprovalsOutliveTheirOperation(t *testing.T) {
	t.Run("restored", func(t *testing.T) {
		ctx := context.Background()
		f := fake.New()
		id, err := f.Send(ctx, `call("confirm_action", {})`, assistant.SendOptions{}, func(assistant.AssistantResponse) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		set, err := agent.NewToolSet(newApprovalTool())
		if err != nil {
			t.Fatal(err)
		}
		e := agent.New(f, agent.Config{ConversationID: id, PermissionsMode: agent.ModeManual})
		defer e.Close()
		saved, err := e.LoadConversation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.InstallConversation(ctx, saved); err != nil {
			t.Fatal(err)
		}
		m := New(e, Config{Tools: set})
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		m.resumePendingTools()
		denyShown(t, m)
	})
	t.Run("stopped while running", func(t *testing.T) {
		tool := newApprovalTool()
		tool.Handler = func(ctx context.Context, _ agent.ToolCall) (agent.ToolResult, error) {
			<-ctx.Done()
			return agent.ToolResult{}, ctx.Err()
		}
		set, err := agent.NewToolSet(tool)
		if err != nil {
			t.Fatal(err)
		}
		m := New(agent.New(fake.New(), agent.Config{PermissionsMode: agent.ModeManual}), Config{Tools: set})
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		setConversationInput(m, `call("confirm_action", {})`)
		_, _ = m.submit()
		for waitingApprovals(m) == 0 {
			_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
		}
		for id, a := range m.approvals {
			a.decide(id, agent.ApprovalAllowOnce)
		}
		for tool := findToolBlock(m.state.Blocks, approvalToolName); tool == nil || tool.Status != agent.ToolRunning; tool = findToolBlock(m.state.Blocks, approvalToolName) {
			_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
		}
		_, stop := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
		drainConversationRemote(t, m)
		runStopCmd(t, m, stop)
		for waitingApprovals(m) == 0 {
			_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
		}
		denyShown(t, m)
	})
}

// denyShown checks m shows one approval panel for a call that holds
// prompts, denies it, and checks the prompts are released.
func denyShown(t *testing.T, m *Model) {
	t.Helper()
	if waitingApprovals(m) != 1 || !agent.SendBlocked(m.engine.State()) {
		t.Fatalf("approval panels = %d, prompts held = %t; want one panel holding prompts", waitingApprovals(m), agent.SendBlocked(m.engine.State()))
	}
	for id, a := range m.approvals {
		if !a.decide(id, agent.ApprovalDeny) {
			t.Fatal("the denial was refused")
		}
	}
	// The conversation may still show the stopped turn ending.
	pumpUntilFree(t, m)
	if agent.SendBlocked(m.engine.State()) {
		t.Fatal("prompts stay held after the denial")
	}
}

// Approving a call the conversation waits on, which a restore found or a
// dropped response left, runs it and releases the prompts it held.
func TestApprovingALeftCallRunsIt(t *testing.T) {
	t.Run("restored", func(t *testing.T) {
		ctx := context.Background()
		f := fake.New()
		id, err := f.Send(ctx, `call("confirm_action", {}); say("confirmed")`, assistant.SendOptions{}, func(assistant.AssistantResponse) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		set, err := agent.NewToolSet(newApprovalTool())
		if err != nil {
			t.Fatal(err)
		}
		e := agent.New(f, agent.Config{ConversationID: id, PermissionsMode: agent.ModeManual})
		defer e.Close()
		saved, err := e.LoadConversation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.InstallConversation(ctx, saved); err != nil {
			t.Fatal(err)
		}
		m := New(e, Config{Tools: set})
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		m.resumePendingTools()
		approveShown(t, m, "confirmed")
	})
	t.Run("dropped at the pause", func(t *testing.T) {
		ws, err := workspace.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ws.Close() })
		set, err := agent.NewToolSet(tools.NewClientTools(ws, exectool.NewExecService(ws.DefaultShellPath()))...)
		if err != nil {
			t.Fatal(err)
		}
		m := New(agent.New(fake.New(), agent.Config{PermissionsMode: agent.ModeManual}), Config{Tools: set, Workspace: ws})
		m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		setConversationInput(m, `load("connection.star", "drop_at_tool_pause"); drop_at_tool_pause()`)
		_, _ = m.submit()
		for waitingApprovals(m) == 0 {
			_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
		}
		approveShown(t, m, "Listed: ")
	})
}

// A call stopped while it ran is offered again; approving it runs it again,
// once, and leaves nothing to resume.
func TestApprovingAStoppedRunningCallRunsItAgain(t *testing.T) {
	var runs atomic.Int32
	entered := make(chan struct{})
	tool := newApprovalTool()
	tool.Handler = func(ctx context.Context, _ agent.ToolCall) (agent.ToolResult, error) {
		if runs.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return agent.ToolResult{}, ctx.Err()
		}
		return agent.ToolResult{Title: "Done", Output: "ok"}, nil
	}
	set, err := agent.NewToolSet(tool)
	if err != nil {
		t.Fatal(err)
	}
	m := New(agent.New(fake.New(), agent.Config{PermissionsMode: agent.ModeManual}), Config{Tools: set})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	setConversationInput(m, `call("confirm_action", {})`)
	_, _ = m.submit()
	for waitingApprovals(m) == 0 {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	for id, a := range m.approvals {
		a.decide(id, agent.ApprovalAllowOnce)
	}
	// The block shows running before the handler starts: stop once both
	// the handler ran and the TUI shows it running.
	waitSignal(t, entered)
	for tool := findToolBlock(m.state.Blocks, approvalToolName); tool == nil || tool.Status != agent.ToolRunning; tool = findToolBlock(m.state.Blocks, approvalToolName) {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	_, stop := m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainConversationRemote(t, m)
	runStopCmd(t, m, stop)
	for waitingApprovals(m) == 0 {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	// Allow for session on the re-offer grants the session, as it does for
	// a call parked in a turn.
	for id, a := range m.approvals {
		if cmd := a.grant(grantSession); cmd != nil {
			t.Fatalf("grant failed: %v", cmd())
		}
		a.decide(id, agent.ApprovalAllowOnce)
	}
	_, _ = m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, State: m.engine.State()}})
	// Bounded: an approval that never runs would start runs forever.
	for i := 0; m.op.busy(); i++ {
		if i == 200 {
			t.Fatal("the approved call keeps starting runs")
		}
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	if n := runs.Load(); n != 2 || m.op.busy() || m.engine.CanResumeTools(set) {
		t.Fatalf("runs = %d, busy = %t, resumable = %t; want the call run again once and nothing left", n, m.op.busy(), m.engine.CanResumeTools(set))
	}
	setConversationInput(m, `call("confirm_action", {})`)
	_, _ = m.submit()
	for m.op.busy() {
		if waitingApprovals(m) != 0 {
			t.Fatal("the session grant did not hold: the same call asks again")
		}
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
	if n := runs.Load(); n != 3 {
		t.Fatalf("runs = %d, want the granted call run without asking", n)
	}
}

// runStopCmd runs the stop request among cmd's commands and applies its
// result.
func runStopCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for cmds := []tea.Cmd{cmd}; len(cmds) > 0; cmds = cmds[1:] {
		if cmds[0] == nil {
			continue
		}
		switch msg := runConversationCmd(t, cmds[0]).(type) {
		case tea.BatchMsg:
			cmds = append(cmds, msg...)
		case stopResultMsg:
			_, _ = m.Update(msg)
		}
	}
}

// approveShown approves the one approval m shows and checks the call ran,
// its answer shows, and nothing holds prompts any more.
func approveShown(t *testing.T, m *Model, answer string) {
	t.Helper()
	if waitingApprovals(m) != 1 {
		t.Fatalf("approval panels = %d, want one", waitingApprovals(m))
	}
	for id, a := range m.approvals {
		if !a.decide(id, agent.ApprovalAllowOnce) {
			t.Fatal("the approval was refused")
		}
	}
	_, _ = m.Update(engineEventMsg{ev: agent.Event{Kind: agent.EventState, State: m.engine.State()}})
	if !m.op.busy() {
		t.Fatal("the approval started no run")
	}
	for !hasTextBlock(m.state.Blocks, answer) || m.op.busy() || m.sendBlocked() {
		_, _ = m.Update(runConversationCmd(t, waitEvent(m.events)))
	}
}

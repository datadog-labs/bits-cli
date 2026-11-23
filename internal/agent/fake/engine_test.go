package fake

import (
	"context"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools"
	exectool "github.com/datadog-labs/bits-cli/internal/tools/exec"
	toolspec "github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// These tests prove the fake speaks the protocol the real engine and tools
// expect. Engine behavior itself is covered by the agent package.

// workspaceTools returns the real client tools over a temp workspace holding
// go.mod and a file containing TODO.
func workspaceTools(t *testing.T) *agent.ToolSet {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"go.mod": "module demo\n", "notes.txt": "TODO: ship\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	commands := exectool.NewExecService(ws.DefaultShellPath())
	t.Cleanup(commands.Close)
	set, err := agent.NewToolSet(tools.NewClientTools(ws, commands)...)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// runTurn runs one turn that must complete, calling decide for each newly
// pending approval, and returns the final blocks and the last backend round.
func runTurn(t *testing.T, engine *agent.Engine, message string, set *agent.ToolSet, onDeny agent.DenyPolicy,
	decide func(agent.Block) agent.ApprovalDecision,
) ([]agent.Block, int) {
	t.Helper()
	settle(t, engine, set, nil)
	decided := map[string]bool{}
	var last agent.Event
	for ev := range follow(t, engine, func() (agent.RunID, error) {
		return engine.StartTurn(context.Background(), agent.TurnInput{Message: message, Tools: set, OnDeny: onDeny})
	}) {
		last = ev
		decideNew(engine, ev.State, decided, decide)
	}
	if last.Kind != agent.EventDone {
		t.Fatalf("last event = %+v, want a completed turn", last)
	}
	settle(t, engine, set, func(s agent.State) { decideNew(engine, s, decided, decide) })
	return engine.State().Blocks, last.Round
}

// decideNew decides each approval s shows for the first time.
func decideNew(engine *agent.Engine, s agent.State, decided map[string]bool, decide func(agent.Block) agent.ApprovalDecision) {
	for _, block := range s.PendingApprovals() {
		if id := block.ToolCallID(); !decided[id] && decide != nil {
			decided[id] = true
			_ = engine.Decide(id, decide(block))
		}
	}
}

// settle waits, as a user would, until the conversation is free: a run
// ends with its response, and the conversation can show the rest of its
// turn after. Each state goes to observe; a call approved meanwhile, such
// as one a dropped response left, runs as the TUI runs it.
func settle(t *testing.T, engine *agent.Engine, set *agent.ToolSet, observe func(agent.State)) {
	t.Helper()
	events, unsubscribe := engine.Subscribe()
	defer unsubscribe()
	for state := engine.State(); ; {
		if observe != nil {
			observe(state)
		}
		if set != nil && !engine.OperationActive() && engine.CanResumeTools(set) {
			for ev := range follow(t, engine, func() (agent.RunID, error) {
				return engine.ResumePendingTools(context.Background(), agent.TurnInput{Tools: set, OnDeny: agent.DenyContinue})
			}) {
				if observe != nil {
					observe(ev.State)
				}
			}
			state = engine.State()
			continue
		}
		followed := state.ConversationID == "" || state.Connection.State == agent.ConnectionConnected
		// A prompt the conversation shows last has a turn still to start,
		// as after a dropped request.
		started := len(state.Blocks) == 0 || state.Blocks[len(state.Blocks)-1].Role != assistant.RoleUser
		if followed && started && !agent.SendBlocked(state) {
			return
		}
		if ev := <-events; ev.Kind == agent.EventState {
			state = ev.State
		}
	}
}

// follow subscribes, starts an operation, and returns its run's events.
func follow(t *testing.T, engine *agent.Engine, start func() (agent.RunID, error)) iter.Seq[agent.Event] {
	t.Helper()
	events, unsubscribe := engine.Subscribe()
	t.Cleanup(unsubscribe)
	run, err := start()
	if err != nil {
		t.Fatal(err)
	}
	return agent.Follow(events, run)
}

func texts(blocks []agent.Block) []string {
	var out []string
	for _, b := range blocks {
		if b.Kind == assistant.KindText && b.Role == assistant.RoleAssistant {
			out = append(out, b.Markdown.Content)
		}
	}
	return out
}

func toolBlock(t *testing.T, blocks []agent.Block, name string) *agent.ToolBlock {
	t.Helper()
	for _, b := range blocks {
		if b.Tool != nil && b.Tool.Name == name {
			return b.Tool
		}
	}
	t.Fatalf("no %s block", name)
	return nil
}

func TestParallelClientCalls(t *testing.T) {
	engine := agent.New(&Fake{}, agent.Config{PermissionsMode: agent.ModeSkipPermissions})
	script := `rs = call([("list_files", {"path": "."}), ("read_file", {"path": "go.mod"}), ("grep_files", {"pattern": "TODO"})]); say(str(["go.mod" in rs[0].output, "module demo" in rs[1].output, "notes.txt" in rs[2].output]))`
	blocks, rounds := runTurn(t, engine, script, workspaceTools(t), agent.DenyContinue, nil)
	if rounds != 2 {
		t.Fatalf("backend rounds = %d, want 2", rounds)
	}
	if got := texts(blocks); !slices.Equal(got, []string{"[True, True, True]"}) {
		t.Fatalf("results reached the script out of order: %q", got)
	}
}

// A connection dropped around a client call that needs approval leaves the
// call's block as a clean run does: approved, with its render state, run once.
// Each run has its own workspace, so render states compare by kind.
func TestDropAroundApprovedClientCall(t *testing.T) {
	run := func(script string) *agent.ToolBlock {
		engine := agent.New(&Fake{}, agent.Config{PermissionsMode: agent.ModeManual})
		approvals := 0
		blocks, _ := runTurn(t, engine, script, workspaceTools(t), agent.DenyContinue, func(agent.Block) agent.ApprovalDecision {
			approvals++
			return agent.ApprovalAllowOnce
		})
		if approvals != 1 || !slices.Equal(texts(blocks), []string{"written"}) {
			t.Fatalf("%s: approvals = %d, answer = %q", script, approvals, texts(blocks))
		}
		for _, b := range blocks {
			if b.Tool != nil && b.Run == 0 {
				t.Fatalf("%s: tool block %s is not the run's", script, b.ID)
			}
		}
		return toolBlock(t, blocks, "write_file")
	}
	const call = `r = call("write_file", {"path": "new.txt", "content": "hello\\n"});`
	clean := run(call + ` say("written" if r.ok else "failed")`)
	if clean.RenderState == nil || clean.Status != agent.ToolSuccess {
		t.Fatalf("clean write = %+v", clean)
	}
	for _, script := range []string{
		`drop(); ` + call + ` say("written" if r.ok else "failed")`,
		call + ` drop(); say("written" if r.ok else "failed")`,
	} {
		got := run(script)
		if got.Status != clean.Status || reflect.TypeOf(got.RenderState) != reflect.TypeOf(clean.RenderState) || got.Detail != clean.Detail || !reflect.DeepEqual(got.Approval, clean.Approval) {
			t.Errorf("%s: write = %+v, want %+v", script, got, clean)
		}
	}
}

// After a stopped turn, the next prompt and its answer show exactly once,
// after what the stopped turn showed, and the stopped turn's rest never shows.
func TestPromptAfterStoppedTurn(t *testing.T) {
	engine := agent.New(&Fake{}, agent.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := false
	for ev := range follow(t, engine, func() (agent.RunID, error) {
		return engine.StartTurn(ctx, agent.TurnInput{Message: `say("before"); sleep("30s"); say("after")`})
	}) {
		if !stopped && slices.Contains(texts(ev.State.Blocks), "before") {
			stopped = true
			if err := engine.StopConversation(context.Background()); err != nil {
				t.Fatal(err)
			}
			cancel()
		}
	}
	blocks, _ := runTurn(t, engine, `say("next")`, nil, agent.DenyContinue, nil)
	var prompts []string
	for _, b := range blocks {
		if b.Role == assistant.RoleUser && b.Markdown != nil {
			prompts = append(prompts, b.Markdown.Content)
		}
	}
	if got := texts(blocks); !slices.Equal(got, []string{"before", "next"}) || len(prompts) != 2 {
		t.Fatalf("answers = %q, prompts = %q", got, prompts)
	}
}

// After a confirmed stop, a client call the stopped turn left unanswered
// stays offered for a decision, and a denial goes out with the next prompt,
// before it, as in the web client. A new conversation drops every decision
// on the old one's calls.
func TestDecisionsAfterStop(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision agent.ApprovalDecision
		fresh    bool // start a new conversation before the next prompt
		want     []string
	}{
		{"denial goes with the next prompt", agent.ApprovalDeny, false, []string{"denial", "prompt"}},
		{"denial stays with its conversation", agent.ApprovalDeny, true, []string{"prompt"}},
		{"approval stays with its conversation", agent.ApprovalAllowOnce, true, []string{"prompt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &Fake{}
			engine := agent.New(f, agent.Config{PermissionsMode: agent.ModeManual})
			defer engine.Close()
			set := workspaceTools(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var call string
			for ev := range follow(t, engine, func() (agent.RunID, error) {
				return engine.StartTurn(ctx, agent.TurnInput{Message: `call("write_file", {"path": "new.txt", "content": "x"}); say("written")`, Tools: set, OnDeny: agent.DenyContinue})
			}) {
				if pending := ev.State.PendingApprovals(); call == "" && len(pending) == 1 {
					call = pending[0].ToolCallID()
					if err := engine.StopConversation(context.Background()); err != nil {
						t.Fatal(err)
					}
					cancel()
				}
			}
			if pending := engine.State().PendingApprovals(); len(pending) != 1 || pending[0].ToolCallID() != call {
				t.Fatalf("pending approvals after the stop = %v, want %s", pending, call)
			}
			if err := engine.Decide(call, tc.decision); err != nil {
				t.Fatal(err)
			}
			if b := toolBlock(t, engine.State().Blocks, "write_file"); tc.decision == agent.ApprovalDeny && b.Status != agent.ToolDenied {
				t.Fatalf("denied call shows %v", b.Status)
			}
			if tc.fresh {
				if err := engine.NewConversation(); err != nil {
					t.Fatal(err)
				}
				if engine.CanResumeTools(set) {
					t.Fatal("the new conversation would answer the old one's call")
				}
			}
			runTurn(t, engine, `say("next")`, set, agent.DenyContinue, nil)
			history, err := f.ConversationHistory(context.Background(), assistant.ConversationHistoryInput{ConversationID: engine.State().ConversationID})
			if err != nil {
				t.Fatal(err)
			}
			var order []string
			for _, m := range history.Data.Attributes.Messages {
				switch {
				case m.Content.Type == assistant.ContentClientToolResponse:
					order = append(order, "denial")
				case assistant.RoleOf(m.Role) == assistant.RoleUser && m.Content.Markdown != nil && m.Content.Markdown.Content == `say("next")`:
					order = append(order, "prompt")
				}
			}
			if !slices.Equal(order, tc.want) {
				t.Fatalf("saved = %q, want %q", order, tc.want)
			}
		})
	}
}

// Answers queued while other turns run show live where saved history puts
// them, with or without a drop at the pause.
func TestQueuedAnswersPlaceAsSavedHistory(t *testing.T) {
	rows := func(blocks []agent.Block) []string {
		var out []string
		for _, b := range blocks {
			switch {
			case b.Markdown != nil:
				out = append(out, fmt.Sprintf("%d:%v:%s", b.TurnNumber, b.Role, b.Markdown.Content))
			case b.Tool != nil:
				out = append(out, fmt.Sprintf("%d:tool:%s", b.TurnNumber, b.ToolCallID()))
			}
		}
		return out
	}
	for _, drop := range []string{"False", "True"} {
		t.Run("drop="+drop, func(t *testing.T) {
			f := &Fake{ScriptRoot: "testdata"}
			e := agent.New(f, agent.Config{PermissionsMode: agent.ModeManual})
			defer e.Close()
			script := fmt.Sprintf(`load("connection.star", "queued_turns_with_tools"); queued_turns_with_tools(delay="20ms", other="200ms", drop_at_pause=%s)`, drop)
			runTurn(t, e, script, workspaceTools(t), agent.DenyContinue, func(agent.Block) agent.ApprovalDecision { return agent.ApprovalAllowOnce })
			live := rows(e.State().Blocks)
			restored := agent.New(f, agent.Config{ConversationID: e.State().ConversationID})
			defer restored.Close()
			for range follow(t, restored, func() (agent.RunID, error) { return restored.Restore(context.Background()) }) {
			}
			if saved := rows(restored.State().Blocks); !slices.Equal(live, saved) {
				t.Fatalf("live = %q\nsaved = %q", live, saved)
			}
		})
	}
}

func TestServerGateWithClientCall(t *testing.T) {
	script := `g, f = call([("approval_request", {"tool_name": "create_monitor", "tool_args": {}}), ("read_file", {"path": "go.mod"})]); say("created" if g.ok else "not created")`
	decide := func(b agent.Block) agent.ApprovalDecision {
		if b.Tool.Name == toolspec.ApprovalRequest {
			return agent.ApprovalDeny
		}
		return agent.ApprovalAllowOnce
	}

	t.Run("deny continue", func(t *testing.T) {
		engine := agent.New(&Fake{}, agent.Config{})
		blocks, _ := runTurn(t, engine, script, workspaceTools(t), agent.DenyContinue, decide)
		if got := texts(blocks); !slices.Equal(got, []string{"not created"}) {
			t.Fatalf("answer = %q", got)
		}
		if read := toolBlock(t, blocks, "read_file"); read.Status != agent.ToolSuccess {
			t.Fatalf("read status = %v", read.Status)
		}
	})
	t.Run("deny stop", func(t *testing.T) {
		engine := agent.New(&Fake{}, agent.Config{})
		blocks, _ := runTurn(t, engine, script, workspaceTools(t), agent.DenyStop, decide)
		if gate := toolBlock(t, blocks, toolspec.ApprovalRequest); gate.Status != agent.ToolDenied || len(texts(blocks)) != 0 {
			t.Fatalf("gate status = %v, answers = %q", gate.Status, texts(blocks))
		}
		// Calls run as they arrive: the read may arrive after the denial,
		// whose stop hides it. Either way it never runs.
		for _, b := range blocks {
			if b.Tool != nil && b.Tool.Name == "read_file" && b.Tool.Status != agent.ToolCancelled {
				t.Fatalf("read status = %v, want cancelled by the denial", b.Tool.Status)
			}
		}
	})
}

// A turn's client calls run once each, through a drop at its pause. A call
// of another session's turn that arrives on this client's open send runs
// here too, as in the web client: the send's response is this client's to
// answer, and the accepted limit is that it cannot tell whose turn made
// the call. One the dropped response never delivered waits for an
// explicit approval, which this test gives.
func TestConnectionScriptsRunCallsOnTheirSendOnce(t *testing.T) {
	for _, tc := range []struct {
		script string
		ran    int
		want   []string
	}{
		{`load("connection.star", "drop_at_tool_pause"); drop_at_tool_pause()`, 1, []string{"Listed: True"}},
		{`load("connection.star", "queued_turns_with_tools"); queued_turns_with_tools(delay="20ms", other="200ms")`, 2, []string{"Listed: True"}},
		{`load("connection.star", "queued_turns_with_tools"); queued_turns_with_tools(delay="20ms", other="200ms", drop_at_pause=False)`, 2, []string{"Listed: True"}},
	} {
		t.Run(tc.script, func(t *testing.T) {
			engine := agent.New(&Fake{ScriptRoot: "testdata"}, agent.Config{PermissionsMode: agent.ModeManual})
			defer engine.Close()
			blocks, _ := runTurn(t, engine, tc.script, workspaceTools(t), agent.DenyContinue, func(agent.Block) agent.ApprovalDecision {
				return agent.ApprovalAllowOnce
			})
			ran := 0
			for _, b := range blocks {
				if b.Tool != nil && b.Tool.Name == "list_files" && b.Tool.Status == agent.ToolSuccess {
					ran++
				}
			}
			got := texts(blocks)
			if ran != tc.ran || slices.ContainsFunc(tc.want, func(s string) bool { return !slices.Contains(got, s) }) {
				t.Fatalf("list_files ran %d times, texts = %q", ran, got)
			}
		})
	}
}

// TestQAScripts runs the bundled QA scripts against the real tools, so schema
// drift fails here rather than during manual QA.
func TestQAScripts(t *testing.T) {
	windows := "False"
	if runtime.GOOS == "windows" {
		windows = "True"
	}
	for _, tc := range []struct {
		script string
		calls  int
	}{
		{`load("disclosure.star", "all"); all(windows=` + windows + `)`, 14},
		{`load("grouping.star", "investigation"); investigation(delay="1ms", windows=` + windows + `)`, 6},
		{`load("exec_wrap.star", "background"); background(windows=` + windows + `)`, 5},
		{`load("code_exec.star", "all"); all(delay="1ms")`, 0},
	} {
		t.Run(tc.script, func(t *testing.T) {
			engine := agent.New(&Fake{ScriptRoot: "testdata"}, agent.Config{PermissionsMode: agent.ModeSkipPermissions})
			blocks, _ := runTurn(t, engine, tc.script, workspaceTools(t), agent.DenyContinue, nil)
			var calls int
			for _, block := range blocks {
				if block.Kind == assistant.KindText && strings.Contains(block.Markdown.Content, "fake script error") {
					t.Fatalf("script failed:\n%s", block.Markdown.Content)
				}
				if block.Tool == nil || !block.Tool.IsClientSide {
					continue
				}
				calls++
				want := agent.ToolSuccess
				if strings.Contains(block.Tool.Input, "missing.txt") || strings.Contains(block.Tool.Input, "does-not-exist") {
					want = agent.ToolError
				}
				if block.Tool.Status != want {
					t.Errorf("%s %s status = %v, want %v: %s", block.Tool.Name, block.Tool.Input, block.Tool.Status, want, block.Tool.Output)
				}
			}
			if calls != tc.calls {
				t.Errorf("ran %d client calls, want %d", calls, tc.calls)
			}
		})
	}
}

func TestKitchen(t *testing.T) {
	engine := agent.New(&Fake{}, agent.Config{PermissionsMode: agent.ModeSkipPermissions})
	blocks, _ := runTurn(t, engine, "kitchen()", workspaceTools(t), agent.DenyContinue, nil)
	kinds := map[assistant.ContentKind]bool{}
	// Shown live, a task's update is a block; a replay shows the task.
	kinds[assistant.KindProgress] = len(engine.State().Tasks) > 0
	styles := chat.DefaultStyles(true)
	for _, block := range blocks {
		kinds[block.Kind] = true
		if strings.TrimSpace(chat.RenderBlock(block, 80, styles, 0)) == "" {
			t.Errorf("block %s rendered empty", block.ID)
		}
	}
	for _, kind := range []assistant.ContentKind{
		assistant.KindText, assistant.KindReasoning, assistant.KindToolCall, assistant.KindWidget,
		assistant.KindDashboard, assistant.KindProgress, assistant.KindMemory,
	} {
		if !kinds[kind] {
			t.Errorf("kitchen produced no %s block", kind)
		}
	}
	if kinds[assistant.KindUnknown] {
		t.Error("kitchen showed its future content type")
	}
}

// TestTaskScript follows a scripted investigation to its final update, live
// and once restored from history.
func TestTaskScript(t *testing.T) {
	f := &Fake{ScriptRoot: "testdata"}
	engine := agent.New(f, agent.Config{})
	t.Cleanup(engine.Close)
	events, unsubscribe := engine.Subscribe()
	t.Cleanup(unsubscribe)
	allow := func(agent.Block) agent.ApprovalDecision { return agent.ApprovalAllowOnce }
	runTurn(t, engine, `load("tasks.star", "investigation"); investigation(every="0s")`, workspaceTools(t), agent.DenyContinue, allow)

	var live agent.Task
	for ev := range events {
		if tasks := ev.State.Tasks; len(tasks) == 1 && tasks[0].Status == agent.TaskDone {
			live = tasks[0]
			break
		}
	}
	if live.Name != "Investigate ad-server error rate on GET /ads" || live.AgentID != "investigation" || live.StartedAt.IsZero() {
		t.Errorf("task = %+v", live)
	}
	if n := len(live.Blocks); n != 7 {
		t.Errorf("task posted %d updates, want 7", n)
	}

	restored := agent.New(f, agent.Config{ConversationID: engine.State().ConversationID})
	t.Cleanup(restored.Close)
	for range follow(t, restored, func() (agent.RunID, error) { return restored.Restore(context.Background()) }) {
	}
	tasks := restored.State().Tasks
	// History times its messages, so the restored task knows when it started.
	if len(tasks) != 1 || tasks[0].Status != agent.TaskDone || len(tasks[0].Blocks) != len(live.Blocks) || tasks[0].StartedAt.IsZero() {
		t.Fatalf("restored tasks = %+v, want the finished task", tasks)
	}
}

package fake

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools"
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
	set, err := agent.NewToolSet(tools.NewClientTools(ws)...)
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
	decided := map[string]bool{}
	var last agent.Event
	for ev := range follow(t, engine, func() (agent.RunID, error) {
		return engine.StartTurn(context.Background(), agent.TurnInput{Message: message, Tools: set, OnDeny: onDeny})
	}) {
		last = ev
		for _, block := range ev.State.PendingApprovals() {
			if id := block.ToolCallID(); !decided[id] {
				decided[id] = true
				_ = engine.Decide(id, decide(block))
			}
		}
	}
	if last.Kind != agent.EventDone {
		t.Fatalf("last event = %+v, want a completed turn", last)
	}
	return engine.State().Blocks, last.Round
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

func TestServerGateWithClientCall(t *testing.T) {
	script := `g, f = call([("approval_request", {"tool_name": "create_monitor", "tool_args": {}}), ("read_file", {"path": "go.mod"})]); say("created" if g.ok else "not created")`
	decide := func(b agent.Block) agent.ApprovalDecision {
		if b.Tool.Name == assistant.ApprovalRequestTool {
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
		if gate := toolBlock(t, blocks, assistant.ApprovalRequestTool); gate.Status != agent.ToolDenied || len(texts(blocks)) != 0 {
			t.Fatalf("gate status = %v, answers = %q", gate.Status, texts(blocks))
		}
		if read := toolBlock(t, blocks, "read_file"); read.Status != agent.ToolCancelled {
			t.Fatal("pending read was not cancelled by the denial")
		}
	})
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
	styles := chat.DefaultStyles(true)
	for _, block := range blocks {
		kinds[block.Kind] = true
		if strings.TrimSpace(chat.RenderBlock(block, 80, styles, 0)) == "" {
			t.Errorf("block %s rendered empty", block.ID)
		}
	}
	for _, kind := range []assistant.ContentKind{
		assistant.KindText, assistant.KindReasoning, assistant.KindToolCall, assistant.KindWidget,
		assistant.KindDashboard, assistant.KindProgress, assistant.KindUnknown,
	} {
		if !kinds[kind] {
			t.Errorf("kitchen produced no %s block", kind)
		}
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

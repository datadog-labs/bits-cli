package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

func TestActivitySnapshotPreservesTaskObservers(t *testing.T) {
	backend := &taskBackend{}
	e, events := newTaskEngine(t, backend)
	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	before := waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 1 })
	user := assistant.AssistantMessage("web-user", assistant.TextContent("from web"))
	user.Role = "user"
	state, err := e.SyncConversationTranscript(testConversationID, NewActivityReplay([]assistant.Message{user, updateMsg("task", "progress", "background")}...), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Blocks) != 1 || len(state.Tasks) != 1 || state.Tasks[0].ID != before[0].ID {
		t.Fatalf("state = %+v", state)
	}
	e.do(func() { e.tasks[0].Name = "updated"; e.publish() })
	if state := e.State(); state.Blocks[0].Markdown.Content != "from web" || state.Tasks[0].Name != "updated" {
		t.Fatalf("state = %+v", state)
	}
}

func TestActivitySnapshotRejectsStaleConversationAndActiveOperation(t *testing.T) {
	backend := &scriptBackend{convID: testConversationID, started: make(chan struct{}), hold: make(chan struct{})}
	e := New(backend, Config{ConversationID: testConversationID})
	defer e.Close()
	if _, err := e.SyncConversationTranscript("old", NewActivityReplay(), false); !errors.Is(err, ErrConversationChanged) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := turnEvents(e, ctx, TurnInput{Message: "local"})
	<-backend.started
	if _, err := e.SyncConversationTranscript(testConversationID, NewActivityReplay(), false); !errors.Is(err, ErrOperationActive) {
		t.Fatal(err)
	}
	cancel()
	collect(events)
}

func TestActivityRecoveryResendsExecutedToolWithoutRunningAgain(t *testing.T) {
	call := clientCallMsg("write-1", "write", `{}`)
	backend := &scriptBackend{convID: testConversationID, rounds: [][]assistant.Message{{call}, nil, {assistant.AssistantMessage("answer", assistant.TextContent("continued"))}}, failOn: 2, err: errors.New("response POST disconnected")}
	e := New(backend, Config{ConversationID: testConversationID})
	defer e.Close()
	runs := 0
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "write"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { runs++; return ToolResult{Output: "written"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	first := collect(turnEvents(e, context.Background(), TurnInput{Message: "write", Tools: tools}))
	if first[len(first)-1].Kind != EventError || runs != 1 {
		t.Fatalf("first=%+v runs=%d", first, runs)
	}
	if _, err := e.SyncConversationTranscript(testConversationID, NewActivityReplay([]assistant.Message{call}...), true); err != nil {
		t.Fatal(err)
	}
	events, stop := e.Subscribe()
	defer stop()
	run, err := e.ResumeToolTurn(context.Background(), ToolRecoveryInput{ConversationID: testConversationID, Calls: []assistant.Content{call.Content}, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	var end Event
	for ev := range Follow(events, run) {
		end = ev
	}
	if end.Kind != EventDone || runs != 1 {
		t.Fatalf("end=%+v runs=%d", end, runs)
	}
	original := backend.sent[1].([]assistant.ClientToolResponse)
	retried := backend.sent[2].([]assistant.ClientToolResponse)
	if len(retried) != 1 || retried[0].Metadata.Output != original[0].Metadata.Output {
		t.Fatalf("retry=%+v original=%+v", retried, original)
	}
	if slices.ContainsFunc(e.State().Blocks, func(b Block) bool { return b.Role == assistant.RoleUser }) {
		t.Fatal("recovery appended an extra user message")
	}
}

func TestActivityRecoveryExecutesUnansweredCall(t *testing.T) {
	call := clientCallMsg("call", "read", `{}`)
	backend := &scriptBackend{convID: testConversationID, rounds: [][]assistant.Message{{call}}, err: errors.New("stream dropped after call")}
	e := New(backend, Config{ConversationID: testConversationID})
	defer e.Close()
	tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "read"}, Handler: func(context.Context, ToolCall) (ToolResult, error) { return ToolResult{Output: "42"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	first := collect(turnEvents(e, context.Background(), TurnInput{Message: "read", Tools: tools}))
	if first[len(first)-1].Kind != EventError {
		t.Fatal("expected interrupted turn")
	}
	if _, err := e.SyncConversationTranscript(testConversationID, NewActivityReplay([]assistant.Message{call}...), true); err != nil {
		t.Fatal(err)
	}
	events, stop := e.Subscribe()
	defer stop()
	run, err := e.ResumeToolTurn(context.Background(), ToolRecoveryInput{ConversationID: testConversationID, Calls: []assistant.Content{call.Content}, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	for ev := range Follow(events, run) {
		if ev.Kind == EventError {
			t.Fatal(ev.Err)
		}
	}
	if got := backend.sent[1].([]assistant.ClientToolResponse); len(got) != 1 || got[0].Metadata.Output != "42" {
		t.Fatalf("response=%+v", got)
	}
}

func TestActivityRecoveryDoesNotRerunInterruptedTool(t *testing.T) {
	for _, resumable := range []bool{false, true} {
		call := clientCallMsg("call", "write", `{}`)
		backend := &scriptBackend{convID: testConversationID, rounds: [][]assistant.Message{{call}, {assistant.AssistantMessage("answer", assistant.TextContent("done"))}}}
		e := New(backend, Config{ConversationID: testConversationID})
		started := make(chan struct{}, 2)
		runs := 0
		tools, err := NewToolSet(Tool{Definition: assistant.ClientTool{Name: "write"}, Resumable: resumable, Handler: func(ctx context.Context, _ ToolCall) (ToolResult, error) {
			runs++
			if runs == 1 {
				started <- struct{}{}
				<-ctx.Done()
				return ToolResult{}, ctx.Err()
			}
			return ToolResult{Output: "written"}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		events := turnEvents(e, ctx, TurnInput{Message: "write", Tools: tools})
		<-started
		cancel()
		collect(events)
		sub, stop := e.Subscribe()
		run, err := e.ResumeToolTurn(context.Background(), ToolRecoveryInput{ConversationID: testConversationID, Calls: []assistant.Content{call.Content}, Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		var end Event
		for ev := range Follow(sub, run) {
			end = ev
		}
		stop()
		got := backend.sent[len(backend.sent)-1].([]assistant.ClientToolResponse)
		if resumable {
			if runs != 2 || got[0].Metadata.Output != "written" {
				t.Fatalf("resumable tool: runs=%d response=%+v", runs, got)
			}
		} else if runs != 1 || got[0].Metadata.Output != interruptedResult().Output {
			t.Fatalf("interrupted tool was rerun: runs=%d response=%+v", runs, got)
		}
		if end.Kind != EventDone {
			t.Fatalf("end=%+v", end)
		}
		if n := get(e, func() int { return len(e.recoveryCalls) + len(e.recoveryResults) + len(e.recoveryStarted) }); n != 0 {
			t.Fatalf("completed turn kept %d recovery entries", n)
		}
		e.Close()
	}
}

func TestActivityRecoveryRequiresToolSet(t *testing.T) {
	e := New(&scriptBackend{}, Config{ConversationID: testConversationID})
	defer e.Close()
	call := clientCallMsg("call", "write", `{}`)
	e.do(func() { e.recoveryCalls = map[string]ToolCall{"call": toolCallOf(call.Content)} })
	if _, err := e.ResumeToolTurn(context.Background(), ToolRecoveryInput{ConversationID: testConversationID, Calls: []assistant.Content{call.Content}}); !errors.Is(err, ErrNoPendingTools) {
		t.Fatal(err)
	}
}

func TestActivityReplayKeepsForkBoundaryAndHistoricalTasks(t *testing.T) {
	const target = "87654321-4321-4321-4321-210987654321"
	messages := startTaskMsgs("start", testTaskID)
	messages = append(messages, clientCallMsg("copied-call", "write", `{}`), updateMsg("boundary", "progress", "copied progress"))
	source := &assistant.ForkedFrom{ConversationID: testConversationID, ForkMessageID: "boundary"}
	b := &taskBackend{}
	e := New(b, Config{})
	t.Cleanup(e.Close)
	conversation := conversationFromHistory(source, messages)
	conversation.id = target
	if err := e.InstallConversation(context.Background(), conversation); err != nil {
		t.Fatal(err)
	}
	before := e.State()
	replay := NewActivityReplay(messages...)
	replay.Append(updateMsg("late-task-result", "final", "copied task completed"))
	replay.Append(assistant.AssistantMessage("live-answer", assistant.TextContent("new answer")))
	state, err := e.SyncConversationTranscript(target, replay, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Fork != before.Fork || len(state.Tasks) != 1 || state.Tasks[0].Status != TaskHistorical {
		t.Fatalf("fork=%+v tasks=%+v", state.Fork, state.Tasks)
	}
	if calls := e.PendingActivityTools(replay); len(calls) != 0 {
		t.Fatalf("copied calls became recoverable: %+v", calls)
	}
	if len(b.attached()) != 0 {
		t.Fatal("subscribed to a copied task")
	}
	for _, block := range state.Blocks[:len(before.Blocks)] {
		if block.ID.Scope != ScopeCopied || (block.Tool != nil && !block.Tool.Status.IsTerminal()) {
			t.Fatalf("lost copied boundary: %+v", block)
		}
	}
	copiedRev := state.Blocks[0].Rev
	last := state.Blocks[len(state.Blocks)-1]
	if last.Markdown.Content != "new answer" || last.Complete {
		t.Fatalf("active replay did not leave the answer streaming: %+v", last)
	}
	replay.Append(assistant.AssistantMessage("live-answer", assistant.TextContent(" continued")))
	state, err = e.SyncConversationTranscript(target, replay, false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Blocks[0].Rev != copiedRev {
		t.Fatal("incremental update revised copied history")
	}
	last = state.Blocks[len(state.Blocks)-1]
	if last.Markdown.Content != "new answer continued" || !last.Complete || state.Fork != before.Fork {
		t.Fatalf("live update changed fork or lost answer: %+v", state)
	}
}

func TestActivityReplayAppliesFragmentsOnceAndKeepsTaskObserver(t *testing.T) {
	b := &taskBackend{}
	e, events := newTaskEngine(t, b)
	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 1 })
	original := get(e, func() *task { return e.tasks[0] })
	replay := NewActivityReplay(assistant.AssistantMessage("answer", assistant.TextContent("first")))
	state, err := e.SyncConversationTranscript(testConversationID, replay, true)
	if err != nil {
		t.Fatal(err)
	}
	rev := state.Blocks[0].Rev
	for range 2 {
		if _, err = e.SyncConversationTranscript(testConversationID, replay, true); err != nil {
			t.Fatal(err)
		}
	}
	if state = e.State(); state.Blocks[0].Rev != rev || state.Blocks[0].Markdown.Content != "first" {
		t.Fatalf("repeated sync duplicated the stream: %+v", state.Blocks[0])
	}
	replay.Append(assistant.AssistantMessage("answer", assistant.TextContent(" second")))
	state, err = e.SyncConversationTranscript(testConversationID, replay, true)
	if err != nil || state.Blocks[0].Markdown.Content != "first second" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if get(e, func() *task { return e.tasks[0] }) != original {
		t.Fatal("foreground synchronization replaced the task observer")
	}
}

func TestActivityReconnectReplayReusesAppliedPrefix(t *testing.T) {
	e := New(&scriptBackend{}, Config{ConversationID: testConversationID})
	t.Cleanup(e.Close)
	first := NewActivityReplay(assistant.AssistantMessage("answer", assistant.TextContent("first")))
	state, err := e.SyncConversationTranscript(testConversationID, first, true)
	if err != nil {
		t.Fatal(err)
	}
	rev := state.Blocks[0].Rev
	again := NewActivityReplay(assistant.AssistantMessage("answer", assistant.TextContent("first")))
	again.Append(assistant.AssistantMessage("answer", assistant.TextContent(" second")))
	state, err = e.SyncConversationTranscript(testConversationID, again, true)
	if err != nil || len(state.Blocks) != 1 || state.Blocks[0].Markdown.Content != "first second" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if state.Blocks[0].Rev == rev {
		t.Fatal("tail was not applied")
	}
	changed := NewActivityReplay(assistant.AssistantMessage("other", assistant.TextContent("rewritten")))
	state, err = e.SyncConversationTranscript(testConversationID, changed, false)
	if err != nil || len(state.Blocks) != 1 || state.Blocks[0].Markdown.Content != "rewritten" {
		t.Fatalf("divergent replay was not rebuilt: state=%+v err=%v", state, err)
	}
}

type closingActivityBackend struct {
	scriptBackend
}

func (*closingActivityBackend) SubscribeConversation(ctx context.Context, _ string, emit func(assistant.ConversationActivity) error) error {
	for {
		if err := emit(assistant.ConversationActivity{Kind: assistant.ConversationActivityMessage}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func TestEngineCloseStopsUnreadActivitySubscription(t *testing.T) {
	e := New(&closingActivityBackend{}, Config{ConversationID: testConversationID})
	updates := e.SubscribeActivity(context.Background(), testConversationID)
	e.Close()
	for range updates {
	}
}

func TestActivityRecoveryRejectsRemoteOrChangedCalls(t *testing.T) {
	call := clientCallMsg("local", "write", `{"path":"original"}`)
	b := &scriptBackend{convID: testConversationID, rounds: [][]assistant.Message{{call}}, err: errors.New("disconnected")}
	e := New(b, Config{ConversationID: testConversationID})
	defer e.Close()
	collect(turnEvents(e, context.Background(), TurnInput{Message: "write"}))
	for _, foreign := range []assistant.Message{
		clientCallMsg("remote", "write", `{}`),
		clientCallMsg("local", "write", `{"path":"changed"}`),
	} {
		replay := NewActivityReplay(foreign)
		if calls := e.PendingActivityTools(replay); len(calls) != 0 {
			t.Fatal("subscription authorized a call not received on the local request")
		}
		_, err := e.ResumeToolTurn(context.Background(), ToolRecoveryInput{ConversationID: testConversationID, Calls: []assistant.Content{foreign.Content}})
		if !errors.Is(err, ErrNoPendingTools) {
			t.Fatalf("foreign recovery = %v", err)
		}
	}
	if len(b.sent) != 1 {
		t.Fatal("foreign recovery sent a request")
	}
}

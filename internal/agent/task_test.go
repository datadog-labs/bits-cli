package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

const testTaskID = "assistant-task.1"

// taskBackend serves conversations and task streams: attachment i replays
// attachments[i]; past the last one, Subscribe waits for cancellation.
type taskBackend struct {
	historyBackend
	attachments [][]assistant.Message
	errs        []error // attachment i ends with errs[i], if set

	mu    sync.Mutex
	calls []string // "conversation/task" per attachment
}

func (b *taskBackend) Subscribe(ctx context.Context, conversation string, opts assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	b.mu.Lock()
	n := len(b.calls)
	b.calls = append(b.calls, conversation+"/"+opts.TaskID)
	b.mu.Unlock()
	if n >= len(b.attachments) {
		<-ctx.Done()
		return ctx.Err()
	}
	for _, msg := range b.attachments[n] {
		if err := fn(msg); err != nil {
			return err
		}
	}
	if n < len(b.errs) {
		return b.errs[n]
	}
	return nil
}

func (b *taskBackend) attached() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.calls)
}

func startTaskMsgs(callID, taskID string) []assistant.Message {
	return []assistant.Message{
		serverCallMsg(callID, startBackgroundTaskTool, `{"task":{"agent_id":"investigation","task_name":"Why 500s"}}`),
		assistant.AssistantMessage(callID+"-result", assistant.ToolResultContent(callID, startBackgroundTaskTool,
			assistant.ToolStatusSuccess, fmt.Sprintf(`{"task_id":%q,"start_result":{}}`, taskID))),
	}
}

func updateMsg(messageID, kind, content string) assistant.Message {
	msg := assistant.AssistantMessage(messageID, assistant.Content{
		Type:     assistant.ContentBackgroundTaskUpdate,
		Progress: &assistant.ProgressPayload{TaskID: testTaskID, EventType: kind, Content: content},
	})
	id := testTaskID
	msg.BackgroundTaskID = &id
	return msg
}

func markerMsg(t string) assistant.Message {
	if t == assistant.ContentReplayComplete {
		return assistant.AssistantMessage(t, assistant.Content{Type: t})
	}
	return assistant.AssistantMessage("marker", assistant.Content{
		Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: t},
	})
}

var replayComplete = markerMsg(assistant.ContentReplayComplete)

// waitTasks reads task events until one satisfies done, and returns it.
func waitTasks(t *testing.T, events <-chan Event, done func([]Task) bool) []Task {
	t.Helper()
	for ev := range events {
		if ev.Kind == EventTasks {
			if ev.Run != 0 {
				t.Fatalf("task event of run %d", ev.Run)
			}
			if done(ev.Tasks) {
				return ev.Tasks
			}
		}
	}
	t.Fatal("subscription ended before the tasks settled")
	return nil
}

func taskSettled(tasks []Task) bool {
	return len(tasks) == 1 && tasks[0].Connection == TaskDetached && tasks[0].Status != TaskRunning
}

func progressTexts(blocks []Block) []string {
	var texts []string
	for _, b := range blocks {
		if b.Progress != nil {
			texts = append(texts, b.Progress.Content)
		}
	}
	return texts
}

func TestTurnStartedTaskIsObservedUntilItsFinalUpdateAndEnd(t *testing.T) {
	backend := &taskBackend{attachments: [][]assistant.Message{{
		updateMsg("u1", "progress", "looking"), replayComplete,
		updateMsg("u2", "final", "found it"), markerMsg("ended"),
	}}}
	backend.convID = testConversationID
	backend.rounds = [][]assistant.Message{startTaskMsgs("call-1", testTaskID)}
	e := New(backend, Config{})
	defer e.Close()
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	tasks := waitTasks(t, events, taskSettled)

	task := tasks[0]
	if task.ID != testTaskID || task.Name != "Why 500s" || task.Agent != "investigation" || task.CallID != "call-1" {
		t.Errorf("task identity = %+v", task)
	}
	if task.Status != TaskDone || task.Err != nil || task.Reconnects != 0 {
		t.Errorf("task = %+v, want done after one attachment", task)
	}
	if got := progressTexts(task.Blocks); !slices.Equal(got, []string{"looking", "found it"}) {
		t.Errorf("task updates = %q", got)
	}
	if task.Progress == nil || task.Progress.Content != "found it" {
		t.Errorf("latest progress = %+v", task.Progress)
	}
	if got := backend.attached(); !slices.Equal(got, []string{testConversationID + "/" + testTaskID}) {
		t.Errorf("attachments = %q", got)
	}
	if blocks := e.Snapshot(); slices.ContainsFunc(blocks, func(b Block) bool { return b.Progress != nil }) {
		t.Error("task updates leaked into the conversation transcript")
	}
}

// A replay replaces what an earlier attachment showed, even when its message
// ids differ, and an end marker without a final update is an idle close.
func TestTaskReplacesContentWithEachCompleteReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &taskBackend{attachments: [][]assistant.Message{
			{updateMsg("u1", "progress", "one"), replayComplete, updateMsg("u2", "progress", "two"), markerMsg("ended")},
			{updateMsg("u1", "progress", "one"), updateMsg("u2-part", "progress", "two")},
			{
				updateMsg("u1", "progress", "one"), updateMsg("u2-part", "progress", "two"), replayComplete,
				updateMsg("u3", "final", "three"), markerMsg("ended"),
			},
		}, errs: []error{nil, errors.New("connection reset")}}
		backend.convID = testConversationID
		backend.rounds = [][]assistant.Message{startTaskMsgs("call-1", testTaskID)}
		e := New(backend, Config{})
		defer e.Close()
		events, unsubscribe := e.Subscribe()
		defer unsubscribe()

		collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
		retrying := waitTasks(t, events, func(tasks []Task) bool {
			return len(tasks) == 1 && tasks[0].Connection == TaskReconnecting && tasks[0].Err != nil
		})
		// The failed attachment never completed its replay: the first one stays shown.
		if got := progressTexts(retrying[0].Blocks); !slices.Equal(got, []string{"one", "two"}) {
			t.Errorf("updates while reconnecting = %q", got)
		}
		task := waitTasks(t, events, taskSettled)[0]
		if task.Status != TaskDone || task.Reconnects != 2 || task.Err != nil {
			t.Errorf("task = %+v, want done after two reconnections", task)
		}
		if got := progressTexts(task.Blocks); !slices.Equal(got, []string{"one", "two", "three"}) {
			t.Errorf("task updates = %q, want the last replay and its live update", got)
		}
	})
}

func TestTaskObservationStopsWhenTheServerRefusesIt(t *testing.T) {
	notFound := &assistant.APIError{StatusCode: 404}
	backend := &taskBackend{attachments: [][]assistant.Message{{}}, errs: []error{notFound}}
	backend.convID = testConversationID
	backend.rounds = [][]assistant.Message{startTaskMsgs("call-1", testTaskID)}
	e := New(backend, Config{})
	defer e.Close()
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	task := waitTasks(t, events, taskSettled)[0]
	if task.Status != TaskFailed || !errors.Is(task.Err, notFound) {
		t.Errorf("task = %+v, want failed with the refusal", task)
	}
}

func TestRestoreScopesTaskMessagesAndObservesUnfinishedTasks(t *testing.T) {
	const doneID = "assistant-task.0"
	doneUpdate := updateMsg("d1", "final", "old result")
	doneUpdate.Content.Progress.TaskID, doneUpdate.BackgroundTaskID = doneID, nil
	history := slices.Concat(
		startTaskMsgs("call-0", doneID), []assistant.Message{doneUpdate},
		startTaskMsgs("call-1", testTaskID), []assistant.Message{updateMsg("u1", "progress", "persisted")},
	)
	backend := &taskBackend{attachments: [][]assistant.Message{{
		updateMsg("u1", "progress", "persisted"), replayComplete, updateMsg("u2", "final", "new result"), markerMsg("ended"),
	}}}
	backend.resp = &assistant.ConversationHistoryResponse{}
	backend.resp.Data.Type = "conversation-history-response"
	backend.resp.Data.Attributes.Messages = history
	e := New(backend, Config{ConversationID: testConversationID})
	defer e.Close()
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	collect(restoreEvents(e, context.Background()))
	tasks := waitTasks(t, events, func(tasks []Task) bool {
		return len(tasks) == 2 && tasks[1].Status == TaskDone
	})

	if tasks[0].ID != doneID || tasks[0].Status != TaskDone || tasks[0].Connection != TaskDetached {
		t.Errorf("finished task = %+v, want done without observation", tasks[0])
	}
	if got := progressTexts(tasks[0].Blocks); !slices.Equal(got, []string{"old result"}) {
		t.Errorf("finished task updates = %q", got)
	}
	if got := progressTexts(tasks[1].Blocks); !slices.Equal(got, []string{"persisted", "new result"}) {
		t.Errorf("observed task updates = %q", got)
	}
	if got := backend.attached(); !slices.Equal(got, []string{testConversationID + "/" + testTaskID}) {
		t.Errorf("attachments = %q, want only the unfinished task", got)
	}
	if blocks := e.Snapshot(); len(blocks) != 2 || slices.ContainsFunc(blocks, func(b Block) bool { return b.Progress != nil }) {
		t.Errorf("conversation transcript = %+v, want the two start calls and no task update", blocks)
	}
}

func TestNewConversationStopsTaskObservers(t *testing.T) {
	backend := &taskBackend{} // attachments never end on their own
	backend.convID = testConversationID
	backend.rounds = [][]assistant.Message{startTaskMsgs("call-1", testTaskID)}
	e := New(backend, Config{})
	defer e.Close()
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 1 && tasks[0].Connection == TaskConnecting })
	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 0 })
	// Close waits for every worker: a stray observer would hang it.
	e.Close()
}

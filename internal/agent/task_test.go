package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

const testTaskID = "assistant-task.1"

// taskBackend serves conversations and task streams: attachment i replays
// attachments[i] then ends with errs[i], if set; past the last attachment,
// Subscribe waits for cancellation.
type taskBackend struct {
	historyBackend
	attachments [][]assistant.Message
	errs        []error

	mu    sync.Mutex
	calls []string    // "conversation/task" per attachment
	at    []time.Time // when each attachment started
}

func (b *taskBackend) Subscribe(ctx context.Context, conversation string, opts assistant.SubscribeOptions, fn func(assistant.Message) error) error {
	b.mu.Lock()
	n := len(b.calls)
	b.calls = append(b.calls, conversation+"/"+opts.TaskID)
	b.at = append(b.at, time.Now())
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

// newTaskEngine returns an engine whose first turn starts testTaskID, and a
// subscription to its events.
func newTaskEngine(t *testing.T, backend *taskBackend) (*Engine, <-chan Event) {
	backend.convID = testConversationID
	backend.rounds = [][]assistant.Message{startTaskMsgs("call-1", testTaskID)}
	e := New(backend, Config{})
	t.Cleanup(e.Close)
	events, unsubscribe := e.Subscribe()
	t.Cleanup(unsubscribe)
	return e, events
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

var (
	replayComplete = assistant.AssistantMessage(assistant.ContentReplayComplete,
		assistant.Content{Type: assistant.ContentReplayComplete})
	taskEnded = assistant.AssistantMessage("marker", assistant.Content{
		Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: "ended"},
	})
)

// waitTasks reads states until their tasks satisfy done, and returns them.
func waitTasks(t *testing.T, events <-chan Event, done func([]Task) bool) []Task {
	t.Helper()
	for ev := range events {
		if ev.Kind == EventState && done(ev.State.Tasks) {
			return ev.State.Tasks
		}
	}
	t.Fatal("subscription ended before the tasks settled")
	return nil
}

func taskEnds(tasks []Task) bool { return len(tasks) == 1 && tasks[0].Status != TaskRunning }

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
		updateMsg("u1", "progress", "looking"), replayComplete, updateMsg("u2", "final", "found it"), taskEnded,
	}}}
	e, events := newTaskEngine(t, backend)

	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	task := waitTasks(t, events, taskEnds)[0]

	if task.ID != testTaskID || task.Name != "Why 500s" || task.CallID != "call-1" {
		t.Errorf("task identity = %+v", task)
	}
	if task.Status != TaskDone || task.Err != nil {
		t.Errorf("task = %+v, want done", task)
	}
	if got := progressTexts(task.Blocks); !slices.Equal(got, []string{"looking", "found it"}) {
		t.Errorf("task updates = %q", got)
	}
	if got := backend.attached(); !slices.Equal(got, []string{testConversationID + "/" + testTaskID}) {
		t.Errorf("attachments = %q", got)
	}
	if blocks := e.State().Blocks; slices.ContainsFunc(blocks, func(b Block) bool { return b.Progress != nil }) {
		t.Error("task updates leaked into the conversation transcript")
	}
}

// Each complete replay replaces what was shown, even when its message ids
// differ; an end marker without a final update is an idle close, and a failed
// attachment is retried. A finished task's stream ends before its replay
// completes, and that replay is shown too.
func TestTaskReconnectsAndShowsEachCompleteReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backend := &taskBackend{attachments: [][]assistant.Message{
			{updateMsg("u1", "progress", "one"), replayComplete, updateMsg("u2", "progress", "two"), taskEnded},
			{updateMsg("u1", "progress", "one"), updateMsg("u2-part", "progress", "two")},
			{updateMsg("u1", "progress", "one"), updateMsg("u2-part", "progress", "two"), updateMsg("u3", "final", "three"), taskEnded},
		}, errs: []error{nil, errors.New("connection reset")}}
		e, events := newTaskEngine(t, backend)

		collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
		retrying := waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 1 && tasks[0].Err != nil })[0]
		// The failed attachment never completed its replay: the first one stays shown.
		if got := progressTexts(retrying.Blocks); retrying.Status != TaskRunning || !slices.Equal(got, []string{"one", "two"}) {
			t.Errorf("retrying task = %v with updates %q", retrying.Status, got)
		}
		task := waitTasks(t, events, taskEnds)[0]
		if task.Status != TaskDone || task.Err != nil || len(backend.attached()) != 3 {
			t.Errorf("task = %+v after %d attachments, want done after 3", task, len(backend.attached()))
		}
		if got := progressTexts(task.Blocks); !slices.Equal(got, []string{"one", "two", "three"}) {
			t.Errorf("task updates = %q, want the last replay only", got)
		}
	})
}

// A stream that fails right after going live backs off like any failure.
func TestTaskBacksOffAStreamThatFailsOnceLive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reset := errors.New("connection reset")
		backend := &taskBackend{
			attachments: [][]assistant.Message{{replayComplete}, {replayComplete}, {replayComplete}, {replayComplete}},
			errs:        []error{reset, reset, reset, reset},
		}
		e, _ := newTaskEngine(t, backend)

		collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
		for len(backend.attached()) < 5 {
			time.Sleep(time.Second)
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		var gaps []time.Duration
		for i := 1; i < len(backend.at); i++ {
			gaps = append(gaps, backend.at[i].Sub(backend.at[i-1]))
		}
		if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}; !slices.Equal(gaps, want) {
			t.Errorf("reconnection gaps = %v, want %v", gaps, want)
		}
	})
}

func TestTaskObservationStopsWhenTheServerRefusesIt(t *testing.T) {
	notFound := &assistant.APIError{StatusCode: 404}
	backend := &taskBackend{attachments: [][]assistant.Message{{}}, errs: []error{notFound}}
	e, events := newTaskEngine(t, backend)

	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	task := waitTasks(t, events, taskEnds)[0]
	if task.Status != TaskFailed || !errors.Is(task.Err, notFound) {
		t.Errorf("task = %+v, want failed with the refusal", task)
	}
}

func TestNewConversationStopsTaskObservers(t *testing.T) {
	backend := &taskBackend{} // attachments never end on their own
	e, events := newTaskEngine(t, backend)

	collect(turnEvents(e, context.Background(), TurnInput{Message: "investigate"}))
	waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 1 })
	if err := e.NewConversation(); err != nil {
		t.Fatal(err)
	}
	waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 0 })
	// Close waits for every worker: a stray observer would hang it.
	e.Close()
}

func TestRestoreFoldsTaskMessagesApartAndObservesUnfinishedTasks(t *testing.T) {
	const doneID = "assistant-task.0"
	doneUpdate := updateMsg("d1", "final", "old result")
	doneUpdate.Content.Progress.TaskID, doneUpdate.BackgroundTaskID = doneID, nil
	history := slices.Concat(
		startTaskMsgs("call-0", doneID), []assistant.Message{doneUpdate},
		startTaskMsgs("call-1", testTaskID), []assistant.Message{updateMsg("u1", "progress", "persisted")},
	)
	backend := &taskBackend{attachments: [][]assistant.Message{{
		updateMsg("u1", "progress", "persisted"), replayComplete, updateMsg("u2", "final", "new result"), taskEnded,
	}}}
	backend.resp = &assistant.ConversationHistoryResponse{}
	backend.resp.Data.Type = "conversation-history-response"
	backend.resp.Data.Attributes.Messages = history
	e := New(backend, Config{ConversationID: testConversationID})
	defer e.Close()
	events, unsubscribe := e.Subscribe()
	defer unsubscribe()

	collect(restoreEvents(e, context.Background()))
	tasks := waitTasks(t, events, func(tasks []Task) bool { return len(tasks) == 2 && tasks[1].Status == TaskDone })

	if tasks[0].ID != doneID || tasks[0].CallID != "call-0" || tasks[0].Status != TaskDone {
		t.Errorf("finished task = %+v, want done", tasks[0])
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
	if blocks := e.State().Blocks; len(blocks) != 2 || slices.ContainsFunc(blocks, func(b Block) bool { return b.Progress != nil }) {
		t.Errorf("conversation transcript = %+v, want the two start calls and no task update", blocks)
	}
}

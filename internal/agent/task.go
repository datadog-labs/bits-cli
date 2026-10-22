package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// TaskBackend observes background tasks. *assistant.Client satisfies it.
type TaskBackend interface {
	Subscribe(ctx context.Context, conversationID string, opts assistant.SubscribeOptions,
		fn func(assistant.Message) error) error
}

// startBackgroundTaskTool is the server tool through which the model starts a
// background task. Its result names the task.
const startBackgroundTaskTool = "startBackgroundTask"

// TaskStatus is where a background task is, as far as the client knows.
type TaskStatus int

const (
	TaskRunning TaskStatus = iota
	TaskDone               // its final update was observed
	TaskFailed             // it can no longer be observed; read Err
)

// TaskConnection is the state of a task's observation stream.
type TaskConnection int

const (
	TaskDetached     TaskConnection = iota // not observed: ended, or the backend cannot observe
	TaskConnecting                         // attached; the replay is not complete yet
	TaskLive                               // the replay is shown and live updates follow
	TaskReconnecting                       // the stream ended early; Err says why, if it failed
)

// Task is a read-only view of a background task: server work that outlives
// the turn that started it and reports into the conversation. It belongs to
// no run: it holds no engine authority and the client cannot cancel it.
type Task struct {
	ID     string
	Name   string
	Agent  string
	CallID string // the startBackgroundTask call that started it, if known
	Status TaskStatus
	// Connection and Reconnects describe the observation. Gaps cannot be read
	// from update sequences, which need not be contiguous; each reconnection
	// replays the task completely, so content is lost only while detached.
	Connection TaskConnection
	Reconnects int
	Err        error
	// Progress is the latest update, if any.
	Progress *assistant.ProgressPayload
	// Blocks are the task's messages: its updates and the turn that reports
	// its outcome, if any. They are scoped to the task, so their ids never
	// collide with the conversation's.
	Blocks []Block
}

// task is the actor's state for one background task.
type task struct {
	Task
	transcript *Transcript // what is shown
	// staging collects an attachment's replay until the server marks it
	// complete. A replay is a full snapshot whose message ids may differ from
	// the live ones, so it replaces the shown transcript and is never merged
	// into it.
	staging *Transcript
}

// taskOf returns the task a successful startBackgroundTask call started.
func taskOf(b Block) (*task, bool) {
	if b.Tool == nil || b.Tool.Name != startBackgroundTaskTool || b.Tool.Status != ToolSuccess {
		return nil, false
	}
	var output struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal([]byte(b.Tool.Output), &output) != nil || output.TaskID == "" {
		return nil, false
	}
	var input struct {
		Task struct {
			AgentID  string `json:"agent_id"`
			TaskName string `json:"task_name"`
		} `json:"task"`
	}
	_ = json.Unmarshal([]byte(b.Tool.Input), &input)
	return &task{
		Task:       Task{ID: output.TaskID, Name: input.Task.TaskName, Agent: input.Task.AgentID, CallID: b.ToolCallID()},
		transcript: NewTranscript(),
	}, true
}

// taskIDOf returns the task a wire message belongs to, if any.
func taskIDOf(msg assistant.Message) string {
	if msg.BackgroundTaskID != nil && *msg.BackgroundTaskID != "" {
		return *msg.BackgroundTaskID
	}
	if msg.Content.Progress != nil {
		return msg.Content.Progress.TaskID
	}
	return ""
}

// discoverTasks returns tasks followed by the tasks started in blocks that it
// does not hold yet, in start order.
func discoverTasks(tasks []*task, blocks []Block) []*task {
	for _, b := range blocks {
		if t, ok := taskOf(b); ok && findTask(tasks, t.ID) == nil {
			tasks = append(tasks, t)
		}
	}
	return tasks
}

func findTask(tasks []*task, id string) *task {
	for _, t := range tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// historyTasks splits a conversation's messages: each task's messages go to
// its own transcript, and the rest is returned. A task with a final update is
// done; history cannot tell whether a turn reporting its outcome is still to
// come, so that turn appears on the next restore.
func historyTasks(messages []assistant.Message) (*Transcript, []*task) {
	transcript := NewTranscript()
	var tasks []*task // the server persists a task's start before its messages
	for _, msg := range messages {
		if id := taskIDOf(msg); id != "" {
			t := findTask(tasks, id)
			if t == nil {
				t = &task{Task: Task{ID: id}, transcript: NewTranscript()}
				tasks = append(tasks, t)
			}
			t.transcript.AppendMessage(msg)
		} else if b, ok := transcript.AppendMessage(msg); ok {
			tasks = discoverTasks(tasks, []Block{b})
		}
	}
	transcript.FinalizeAll()
	for _, t := range tasks {
		t.transcript.FinalizeAll()
		if finalUpdate(t.transcript.Blocks()) {
			t.Status = TaskDone
		}
	}
	return transcript, tasks
}

func finalUpdate(blocks []Block) bool {
	return slices.ContainsFunc(blocks, func(b Block) bool {
		return b.Progress != nil && b.Progress.EventType == "final"
	})
}

// view copies t for subscribers.
func (t *task) view() Task {
	v := t.Task
	v.Blocks = slices.Clone(t.transcript.Blocks())
	for _, b := range slices.Backward(v.Blocks) {
		if b.Progress != nil {
			v.Progress = b.Progress
			break
		}
	}
	return v
}

// Tasks returns the current conversation's background tasks, in start order.
func (e *Engine) Tasks() []Task {
	return get(e, e.taskViews)
}

func (e *Engine) taskViews() []Task {
	views := make([]Task, len(e.tasks))
	for i, t := range e.tasks {
		views[i] = t.view()
	}
	return views
}

// publishTasks emits the tasks, outside any run.
func (e *Engine) publishTasks() {
	e.emit(0, Event{Kind: EventTasks, Tasks: e.taskViews()})
}

// setTasks replaces the conversation's tasks, stops the previous observers
// and observes the running tasks.
func (e *Engine) setTasks(tasks []*task) {
	e.stopObserving()
	e.observing, e.stopObserving = context.WithCancel(e.life)
	changed := len(e.tasks) > 0 || len(tasks) > 0
	e.tasks = nil
	e.addTasks(tasks)
	if changed {
		e.publishTasks()
	}
}

// addTasks adds tasks and observes the running ones. The caller publishes.
func (e *Engine) addTasks(tasks []*task) {
	e.tasks = append(e.tasks, tasks...)
	backend, ok := e.backend.(TaskBackend)
	conversation := e.opts.ConversationID
	if !ok || conversation == "" || e.life.Err() != nil {
		return
	}
	for _, t := range tasks {
		if t.Status == TaskRunning {
			t.Connection = TaskConnecting
			e.workers.Add(1)
			e.work(e.observing, func(ctx context.Context) { e.observe(ctx, backend, conversation, t) })
		}
	}
}

// discover adds the tasks a turn started, from its first block.
func (e *Engine) discover(from int) {
	n := len(e.tasks)
	if tasks := discoverTasks(e.tasks, e.transcript.Blocks()[from:]); len(tasks) > n {
		e.tasks = tasks[:n:n]
		e.addTasks(tasks[n:])
		e.publishTasks()
	}
}

// errTaskEnded stops reading a stream at the task's end marker.
var errTaskEnded = errors.New("task ended")

const (
	taskRetryMin = time.Second
	taskRetryMax = 30 * time.Second
)

// observe follows t until it ends, the conversation changes or the engine
// closes. Each attachment stages the replay and shows it at replay_complete.
// The server sends an end marker when the task ends but also when its stream
// idles out, so the task is done only once its final update was seen too.
// Observation only folds content: it never runs tools or decides approvals.
func (e *Engine) observe(ctx context.Context, backend TaskBackend, conversation string, t *task) {
	var delay time.Duration
	for attempt := 0; ; attempt++ {
		e.updateTask(t, func() bool {
			t.staging, t.Connection = NewTranscript(), TaskConnecting
			t.Reconnects += min(attempt, 1)
			return true
		})
		var final, ended, live bool
		err := backend.Subscribe(ctx, conversation, assistant.SubscribeOptions{TaskID: t.ID}, func(msg assistant.Message) error {
			if status := msg.Content.TurnStatus; status != nil && status.Status == "ended" {
				ended = true
				return errTaskEnded
			}
			final = final || msg.Content.Progress != nil && msg.Content.Progress.EventType == "final"
			live = live || msg.Content.Type == assistant.ContentReplayComplete
			e.updateTask(t, func() bool { return t.apply(msg) })
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		status := TaskRunning
		var apiErr *assistant.APIError
		switch {
		case ended && final:
			status, err = TaskDone, nil
		case errors.As(err, &apiErr) && slices.Contains([]int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound}, apiErr.StatusCode):
			status = TaskFailed
		case errors.Is(err, errTaskEnded):
			err = nil
		}
		e.updateTask(t, func() bool {
			t.staging, t.Err = nil, err
			t.Connection = TaskReconnecting
			if status != TaskRunning {
				t.Status, t.Connection = status, TaskDetached
				t.transcript.FinalizeAll()
			}
			return true
		})
		if status != TaskRunning {
			return
		}
		// The server idles a stream out after two minutes, so reconnecting
		// soon after a live attachment cannot spin; failures back off.
		delay = min(max(2*delay, taskRetryMin), taskRetryMax)
		if live {
			delay = taskRetryMin
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// updateTask applies change to t on the actor and publishes the tasks if t
// changed and still belongs to the current conversation.
func (e *Engine) updateTask(t *task, change func() bool) {
	e.do(func() {
		if change() && slices.Contains(e.tasks, t) {
			e.publishTasks()
		}
	})
}

// apply folds a message of the current attachment, reporting whether the
// shown content changed.
func (t *task) apply(msg assistant.Message) bool {
	switch {
	case msg.Content.Type == assistant.ContentReplayComplete:
		t.transcript, t.staging = t.staging, nil
		t.Connection, t.Err = TaskLive, nil
		return true
	case t.staging != nil:
		t.staging.AppendMessage(msg)
		return false
	default:
		_, ok := t.transcript.AppendMessage(msg)
		return ok
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
)

// TaskBackend observes background tasks. *assistant.Client satisfies it.
type TaskBackend interface {
	Subscribe(ctx context.Context, conversationID string, opts assistant.SubscribeOptions,
		fn func(assistant.Message) error) error
}

// TaskStatus is where a background task is, as far as the client knows.
type TaskStatus int

const (
	TaskRunning    TaskStatus = iota
	TaskDone                  // its final update was observed
	TaskFailed                // the server refused to show it; read Err
	TaskHistorical            // copied from another conversation; no worker belongs to this one
)

// Task is a background task: server work that a turn started and that reports
// into the conversation after the turn ended. It belongs to no run: the client
// observes it but can neither steer nor cancel it.
type Task struct {
	ID     string
	Name   string
	CallID string // the startBackgroundTask call that started it, if known
	Status TaskStatus
	// Err is why the last observation failed: the engine retries while the
	// task is running, and gives up once it failed.
	Err error
	// Blocks are the task's messages: its updates, and the turn reporting its
	// outcome if any. They are folded apart from the conversation, whose turns
	// own their range of blocks, and a reconnection replaces them whole.
	Blocks []Block
}

// task is the actor's state for one background task.
type task struct {
	Task
	transcript *Transcript
	stop       context.CancelFunc // ends its observer, if any
}

func newTask(id string) *task {
	return &task{Task: Task{ID: id}, transcript: NewTranscript(), stop: func() {}}
}

// taskOf returns the task a successful startBackgroundTask call started.
func taskOf(b Block) (*task, bool) {
	if b.ID.Scope == ScopeCopied || b.Tool == nil || b.Tool.IsClientSide || b.Tool.Name != spec.StartBackgroundTask || b.Tool.Status != ToolSuccess {
		return nil, false
	}
	var output struct {
		TaskID string `json:"task_id"`
	}
	if json.Unmarshal([]byte(b.Tool.Output), &output) != nil || output.TaskID == "" {
		return nil, false
	}
	var input spec.StartBackgroundTaskInput
	_ = json.Unmarshal([]byte(b.Tool.Input), &input)
	t := newTask(output.TaskID)
	t.Name, t.CallID = input.Task.TaskName, b.ToolCallID()
	return t, true
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

func isFinalUpdate(msg assistant.Message) bool {
	return msg.Content.Progress != nil && msg.Content.Progress.EventType == "final"
}

func findTask(tasks []*task, id string) *task {
	for _, t := range tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// startedTasks appends to tasks those started in blocks that it lacks.
func startedTasks(tasks []*task, blocks []Block) []*task {
	for _, b := range blocks {
		if t, ok := taskOf(b); ok && findTask(tasks, t.ID) == nil {
			tasks = append(tasks, t)
		}
	}
	return tasks
}

// historyTasks folds a conversation's messages: each task's messages into the
// task, the rest into the returned transcript. A task whose final update is
// persisted is done; one whose start call is missing is kept, unnamed.
func historyTasks(messages []assistant.Message) (*Transcript, []*task) {
	transcript := NewTranscript()
	for _, msg := range messages {
		if taskIDOf(msg) == "" {
			transcript.AppendMessage(msg)
		}
	}
	transcript.FinalizeAll()
	tasks := startedTasks(nil, transcript.Blocks())
	for _, msg := range messages {
		id := taskIDOf(msg)
		if id == "" {
			continue
		}
		t := findTask(tasks, id)
		if t == nil {
			t = newTask(id)
			tasks = append(tasks, t)
		}
		t.transcript.AppendMessage(msg)
		if isFinalUpdate(msg) {
			t.Status = TaskDone
		}
	}
	for _, t := range tasks {
		t.transcript.FinalizeAll()
	}
	return transcript, tasks
}

// taskViews copies the tasks for a state.
func (e *Engine) taskViews() []Task {
	var tasks []Task
	for _, t := range e.tasks {
		view := t.Task
		view.Blocks = slices.Clone(t.transcript.Blocks())
		tasks = append(tasks, view)
	}
	return tasks
}

// setTasks replaces the conversation's tasks, stopping their observers. The
// caller publishes.
func (e *Engine) setTasks(tasks []*task) {
	for _, t := range e.tasks {
		t.stop()
	}
	e.tasks = nil
	e.addTasks(tasks)
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
			var ctx context.Context
			ctx, t.stop = context.WithCancel(context.Background())
			e.workers.Add(1)
			e.work(ctx, func(ctx context.Context) { e.observe(ctx, backend, conversation, t) })
		}
	}
}

// discoverTasks adds the tasks the transcript started that the engine lacks.
// The caller publishes.
func (e *Engine) discoverTasks() {
	n := len(e.tasks)
	if tasks := startedTasks(e.tasks, e.transcript.Blocks()); len(tasks) > n {
		e.tasks = tasks[:n:n]
		e.addTasks(tasks[n:])
	}
}

// errTaskEnded stops reading a stream at the task's end marker.
var errTaskEnded = errors.New("task ended")

const (
	taskRetryMin = time.Second
	taskRetryMax = 30 * time.Second
)

// observe follows t until it is done, the server refuses it, or its context
// ends. Each attachment replays the task in full: the replay is folded off the
// actor and replaces the task's blocks at once, since its message ids may
// differ from the live ones. The server sends the end marker when the task
// ended but also when the stream idles out, so the task is done only if its
// final update came too. Observation only folds content: it never runs tools
// nor decides approvals.
func (e *Engine) observe(ctx context.Context, backend TaskBackend, conversation string, t *task) {
	delay := taskRetryMin
	for {
		replay := NewTranscript()
		var live, final, ended bool
		// Until live, replay is the observer's: the actor gets it whole.
		show := func() {
			live = true
			e.updateTask(t, func() bool {
				replay.supersede(t.transcript)
				t.transcript, t.Err = replay, nil
				return true
			})
		}
		err := backend.Subscribe(ctx, conversation, assistant.SubscribeOptions{TaskID: t.ID}, func(msg assistant.Message) error {
			final = final || isFinalUpdate(msg)
			switch {
			case msg.Content.TurnStatus != nil && msg.Content.TurnStatus.Status == "ended":
				// A finished task's stream ends before its replay completes.
				if !live {
					show()
				}
				ended = true
				return errTaskEnded
			case msg.Content.Type == assistant.ContentReplayComplete:
				show()
			case !live:
				replay.AppendMessage(msg)
			default:
				e.updateTask(t, func() bool { _, ok := t.transcript.AppendMessage(msg); return ok })
			}
			return nil
		})
		if ctx.Err() != nil {
			return
		}
		var apiErr *assistant.APIError
		refused := errors.As(err, &apiErr) && slices.Contains(
			[]int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound}, apiErr.StatusCode)
		status := TaskRunning
		switch {
		case final && ended:
			status, err = TaskDone, nil
		case refused:
			status = TaskFailed
		case errors.Is(err, errTaskEnded):
			err = nil
		}
		e.updateTask(t, func() bool {
			t.Status, t.Err = status, err
			if status != TaskRunning {
				t.transcript.FinalizeAll()
			}
			return true
		})
		if status != TaskRunning {
			return
		}
		// The server idles a healthy stream out after two minutes, so
		// reconnecting soon after one cannot spin. Failures back off, even
		// once the stream went live: a stream can fail right after its replay.
		if live && err == nil {
			delay = taskRetryMin
		}
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		delay = min(2*delay, taskRetryMax)
	}
}

// updateTask applies change to t on the actor and publishes the state if t
// changed and still belongs to the conversation.
func (e *Engine) updateTask(t *task, change func() bool) {
	e.do(func() {
		if change() && slices.Contains(e.tasks, t) {
			e.publish()
		}
	})
}

// BlocksWithTasks shows each background task's blocks right after the call that
// started it, so a task reads the same live and restored however the
// conversation went on. A task whose call is not shown goes last.
func BlocksWithTasks(blocks []Block, tasks []Task) []Block {
	if len(tasks) == 0 {
		return blocks
	}
	out := make([]Block, 0, len(blocks))
	placed := make([]bool, len(tasks))
	for _, b := range blocks {
		out = append(out, b)
		for i, task := range tasks {
			if task.CallID != "" && task.CallID == b.ToolCallID() {
				out = append(out, task.Blocks...)
				placed[i] = true
			}
		}
	}
	for i, task := range tasks {
		if !placed[i] {
			out = append(out, task.Blocks...)
		}
	}
	return out
}

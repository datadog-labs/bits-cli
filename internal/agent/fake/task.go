package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"go.starlark.net/starlark"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	toolspec "github.com/datadog-labs/bits-cli/internal/tools/spec"
)

// task() emits a server call, so it joins the built-ins at init time like
// push_conversation().
func init() {
	builtins["task"] = starlark.NewBuiltin("task", builtinTask)
}

// fakeTask is a background task a script started. Like a server task, it runs
// apart from whoever follows it: its updates come due one every interval after
// it started, then its final update.
type fakeTask struct {
	id      string
	agentID string
	started time.Time
	every   time.Duration
	updates []string // progress updates, in order
	status  string   // the final update's outcome, such as "succeeded"
}

// posts is how many updates the task has: its progress ones and its final one.
func (t *fakeTask) posts() int { return len(t.updates) + 1 }

// at is when update i comes due.
func (t *fakeTask) at(i int) time.Time { return t.started.Add(time.Duration(i+1) * t.every) }

// due is how many updates have come due at now.
func (t *fakeTask) due(now time.Time) int {
	n := 0
	for n < t.posts() && !t.at(n).After(now) {
		n++
	}
	return n
}

// update is update i as the server sends it. Only persisted messages carry
// their time: a live one has none, as on the real stream.
func (t *fakeTask) update(i int, persisted bool) assistant.Message {
	progress := &assistant.ProgressPayload{
		TaskID:       t.id,
		Sequence:     i + 1,
		EventType:    "progress",
		TaskMetadata: map[string]string{"agent_id": t.agentID},
	}
	if i < len(t.updates) {
		progress.Content = t.updates[i]
	} else {
		progress.EventType, progress.Content = "final", "Task complete."
		progress.TaskMetadata["status"] = t.status
	}
	msg := assistant.AssistantMessage(t.id+"-update-"+strconv.Itoa(i+1),
		assistant.Content{Type: assistant.ContentBackgroundTaskUpdate, Progress: progress})
	msg.BackgroundTaskID = &t.id
	if persisted {
		msg.CreatedAt = t.at(i).UnixMilli()
	}
	return msg
}

// builtinTask starts a background task: the startBackgroundTask call and its
// result, then updates that come due as the task runs. It returns the task id.
func builtinTask(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name string
	var updates *starlark.List
	agentID, every, status := "investigation", "2s", "succeeded"
	if err := starlark.UnpackArgs(b.Name(), args, kwargs,
		"name", &name, "updates", &updates, "agent?", &agentID, "every?", &every, "status?", &status); err != nil {
		return nil, err
	}
	interval, err := time.ParseDuration(every)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.Name(), err)
	}
	t := &fakeTask{agentID: agentID, every: interval, status: status}
	for i := range updates.Len() {
		update, ok := starlark.AsString(updates.Index(i))
		if !ok {
			return nil, fmt.Errorf("%s: update %d is %s, want string", b.Name(), i, updates.Index(i).Type())
		}
		t.updates = append(t.updates, update)
	}
	r := runOf(thread)
	if err := r.sideEffectError(b.Name()); err != nil {
		return nil, err
	}
	if id, ok := r.replayedID(); ok {
		return starlark.String(id), nil
	}
	input, err := json.Marshal(map[string]any{"task": map[string]string{"agent_id": agentID, "task_name": name}})
	if err != nil {
		return nil, err
	}
	call := spec{id: r.out.nextID(), name: toolspec.StartBackgroundTask, input: string(input)}
	t.id = "fake-task." + call.id
	call.out = fmt.Sprintf(`{"task_id":%q,"start_result":{}}`, t.id)
	if err := r.emitToolCall(call, toolEmit{}); err != nil {
		return nil, err
	}
	// Started before its result is emitted: the engine follows a task as
	// soon as it sees the result.
	t.started = time.Now()
	r.fake.startTask(r.conv, t)
	r.fake.rememberID(r.conv, t.id)
	if err := r.emitToolResults([]spec{call}, assistant.ToolStatusSuccess, "", ""); err != nil {
		return nil, err
	}
	return starlark.String(t.id), nil
}

func (f *Fake) startTask(c *conversation, t *fakeTask) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.tasks = append(c.tasks, t)
}

// taskHistoryLocked is the updates c's tasks have posted by now, as history
// persists them.
func taskHistoryLocked(c *conversation, now time.Time) []assistant.Message {
	var messages []assistant.Message
	for _, t := range c.tasks {
		for i := range t.due(now) {
			messages = append(messages, t.update(i, true))
		}
	}
	return messages
}

// Subscribe follows one task as the server does: it replays the updates
// posted so far, marks the replay complete, sends the rest as they come due,
// and ends with the end marker. An unknown task is not found.
func (f *Fake) Subscribe(ctx context.Context, conversationID string, opts assistant.SubscribeOptions,
	fn func(assistant.Message) error,
) error {
	if opts.TaskID == "" {
		return errors.New("fake: subscribe follows one task")
	}
	t := f.findTask(conversationID, opts.TaskID)
	if t == nil {
		return &assistant.APIError{StatusCode: http.StatusNotFound, Title: "Not Found"}
	}
	posted := t.due(time.Now())
	for i := range posted {
		if err := fn(t.update(i, true)); err != nil {
			return err
		}
	}
	if err := fn(assistant.AssistantMessage(assistant.ContentReplayComplete,
		assistant.Content{Type: assistant.ContentReplayComplete})); err != nil {
		return err
	}
	for i := posted; i < t.posts(); i++ {
		timer := time.NewTimer(time.Until(t.at(i)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := fn(t.update(i, false)); err != nil {
			return err
		}
	}
	return fn(assistant.AssistantMessage(t.id+"-ended", assistant.Content{
		Type: assistant.ContentTurnStatus, TurnStatus: &assistant.TurnStatusPayload{Status: "ended"},
	}))
}

func (f *Fake) findTask(conversationID, taskID string) *fakeTask {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.convs[conversationID]
	if !ok {
		return nil
	}
	for _, t := range c.tasks {
		if t.id == taskID {
			return t
		}
	}
	return nil
}

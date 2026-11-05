package graphics

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// Queue orders terminal image writes across Bubble Tea updates. Its zero value
// is ready to use.
type Queue struct {
	pending []queuedWrite
	busy    bool
	after   tea.Cmd
}

type queuedWrite struct {
	queue *Queue
	data  any
}

func (w queuedWrite) String() string { return fmt.Sprint(w.data) }

// Enqueue accepts a command returned by Image.Transmit, Image.Place, or Delete.
// It returns a command only when the queue was idle.
func (q *Queue) Enqueue(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	raw, ok := cmd().(tea.RawMsg)
	if !ok {
		panic("graphics.Queue: expected a raw image command")
	}
	q.pending = append(q.pending, queuedWrite{queue: q, data: raw.Msg})
	return q.next()
}

// Handle advances the queue after Bubble Tea has processed its RawMsg.
func (q *Queue) Handle(msg tea.Msg) (tea.Cmd, bool) {
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		return nil, false
	}
	write, ok := raw.Msg.(queuedWrite)
	if !ok || write.queue != q {
		return nil, false
	}
	q.busy = false
	if cmd := q.next(); cmd != nil {
		return cmd, true
	}
	cmd := q.after
	q.after = nil
	return cmd, true
}

// DrainThen runs cmd after all queued writes, including deletion on quit.
func (q *Queue) DrainThen(cmd tea.Cmd) tea.Cmd {
	if !q.busy && len(q.pending) == 0 {
		return cmd
	}
	q.after = cmd
	return nil
}

func (q *Queue) next() tea.Cmd {
	if q.busy || len(q.pending) == 0 {
		return nil
	}
	write := q.pending[0]
	q.pending[0] = queuedWrite{}
	q.pending = q.pending[1:]
	q.busy = true
	// Bubble Tea processes a RawMsg before passing it to Model.Update. Handle
	// starts the next write only after that happens.
	return tea.Raw(write)
}

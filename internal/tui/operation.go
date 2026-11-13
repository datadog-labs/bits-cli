package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

// operation is the one exclusive session operation: a turn (resumed client
// tools included), a history restore, a fork, a rename, or logout. Admission
// and follow-ups read this state; chatPhase only drives what the chat displays.
type operation struct {
	kind   opKind
	gen    uint64      // stamps the operation's messages; never reset, so stale ones drop
	run    agent.RunID // the engine run, while one streams; zero otherwise
	cancel context.CancelFunc
	stop   stopLevel
	then   followUp
	// completed means the shown turn ended successfully. Prompts queued after
	// its end was emitted, before it was read, are then sent here.
	completed bool
}

type opKind uint8

const (
	opIdle    opKind = iota
	opTurn           // a user turn or resumed client tools
	opRename         // rename the current saved conversation
	opFork           // create and load a conversation copy in place
	opRestore        // conversation history loading at startup
	opLogout         // credential deletion; the session quits once it succeeds
)

// stopLevel only rises within an operation: stopping its tools never undoes a
// full cancel.
type stopLevel uint8

const (
	stopNone  stopLevel = iota
	stopTools           // the engine stops client tools; the turn continues
	stopAll             // the operation's context is cancelled
)

// followUp runs once the operation ends. It only rises, so a queued logout
// wins over a queued new conversation.
type followUp uint8

const (
	thenNothing followUp = iota
	thenNewConversation
	thenLogout
)

func (o operation) busy() bool { return o.kind != opIdle }

func (o operation) loggingOut() bool { return o.kind == opLogout || o.then == thenLogout }

// task owns a replaceable background lifetime, possibly shared by requests.
// Results carry its generation; it does not claim the exclusive operation.
// Starting or stopping advances the generation, so a result from an earlier
// request drops.
type task struct {
	gen    uint64
	cancel context.CancelFunc
}

// start stops any running request and returns the next one's context and
// generation. A zero timeout means no deadline.
func (t *task) start(parent context.Context, timeout time.Duration) (context.Context, uint64) {
	t.stop()
	var ctx context.Context
	if timeout > 0 {
		ctx, t.cancel = context.WithTimeout(parent, timeout)
	} else {
		ctx, t.cancel = context.WithCancel(parent)
	}
	return ctx, t.gen
}

// stop cancels the running request and invalidates its results.
func (t *task) stop() {
	t.gen++
	t.done()
}

// done releases a finished request's context; its results stay current.
func (t *task) done() {
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
}

func (t *task) running() bool { return t.cancel != nil }

// stopBackgroundTasks invalidates independently owned requests before shutdown
// or credential deletion. File search additionally needs an asynchronous join.
func (m *Model) stopBackgroundTasks() tea.Cmd {
	m.startupTask.stop()
	m.conversationTask.stop()
	m.resume.task.stop()
	m.skillMenu.task.stop()
	m.statusTask.stop()
	m.stopEntitySearch()
	return m.stopFileSearch()
}

// claim reserves the exclusive slot before any side effect starts.
// Cancellation keeps this slot occupied until the terminal result arrives.
func (o *operation) claim(kind opKind, cancel context.CancelFunc) bool {
	if o.busy() {
		return false
	}
	*o = operation{kind: kind, gen: o.gen + 1, cancel: cancel}
	return true
}

func (o operation) matches(kind opKind, generation uint64) bool {
	return o.kind == kind && o.gen == generation
}

func (o operation) interrupted() bool {
	return o.stop == stopAll || o.then != thenNothing
}

// cancelOnce requests shutdown without releasing admission or invalidating the
// terminal result. User cancellation policy is checked by Model.cancellable.
func (o *operation) cancelOnce() {
	if !o.busy() || o.stop == stopAll {
		return
	}
	o.stop = stopAll
	if o.cancel != nil {
		o.cancel()
	}
}

// finish releases resources without resetting the identity counter.
func (o *operation) finish() operation {
	done := *o
	*o = operation{gen: done.gen}
	if done.stop != stopAll && done.cancel != nil {
		done.cancel()
	}
	return done
}

// begin admits every exclusive operation through the same gate.
func (m *Model) begin(kind opKind, cancel context.CancelFunc) bool {
	if m.op.claim(kind, cancel) {
		m.terminal.outcome = ""
		return true
	}
	if cancel != nil {
		cancel()
	}
	m.postNotice(notice(chat.NoticeWarn, nil, "Wait for the current operation to finish."))
	return false
}

// watchRun attaches an engine run to the operation already admitted by begin.
func (m *Model) watchRun(run agent.RunID, err error) tea.Cmd {
	m.op.run = run
	if err != nil {
		return m.applyRunEvent(agent.Event{Kind: agent.EventError, Err: err})
	}
	return nil
}

// endOperation ends an exclusive operation after its terminal result, then
// runs what was queued behind it.
func (m *Model) endOperation() tea.Cmd {
	done := m.op.finish()
	// Only operations that drive the chat phase reset it on completion.
	if done.kind == opTurn || done.kind == opRestore || done.kind == opFork {
		if m.chatPhase != chat.PhaseError {
			m.chatPhase = chat.PhaseIdle
		}
	}
	if done.kind == opTurn || done.kind == opRestore {
		m.clearAsks()
	}
	if m.localTurnOwned && (!m.localTurnRecoverable || done.stop != stopNone || done.then != thenNothing) {
		m.clearLocalTurnOwnership()
	}

	m.syncStatus()
	switch {
	case done.then == thenLogout:
		return m.startLogout()
	case done.then == thenNewConversation:
		return m.startNewConversation()
	case done.kind == opRestore && done.stop != stopAll:
		return tea.Batch(m.startConversationSubscription(m.state.ConversationID), m.resumePendingTools())
	case done.kind == opTurn && done.completed && done.stop == stopNone && len(m.state.Queued) > 0:
		var attach tea.Cmd
		if !m.activityAttached() {
			attach = m.startConversationSubscription(m.state.ConversationID)
		}
		return tea.Batch(attach, m.startTurn(m.engine.SendQueued, agent.TurnInput{}))
	case done.kind == opTurn && (m.localTurnRecoverable || !m.activityAttached()):
		// Recover only from a fresh replay after a failed local request. A
		// healthy attachment already carries a completed turn.
		return m.startConversationSubscription(m.state.ConversationID)
	}
	return m.maybeResumeLocalTurn()
}

// cancellable reports whether an operation is running that cancelOperation
// has not already stopped.
func (m *Model) cancellable() bool {
	return m.op.busy() && m.op.kind != opLogout && m.op.stop != stopAll
}

// cancelOperation requests cancellation once, keeping admission closed until
// completion. Only engine operations own tool UIs. Logout is not interruptible.
func (m *Model) cancelOperation() {
	if !m.cancellable() {
		return
	}
	m.op.cancelOnce()
	if m.op.kind == opTurn || m.op.kind == opRestore {
		m.dropToolUIs()
	}
}

// after queues next for when the running operation ends, and cancels the
// operation so that happens soon.
func (m *Model) after(next followUp) {
	m.op.then = max(m.op.then, next)
	m.cancelOperation()
}

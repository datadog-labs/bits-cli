package tui

import (
	"reflect"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// TestSubmitStampsFinishOnTerminalEvent confirms a user-submitted turn's
// finish instant lands in runFinished once its terminal event applies.
func TestSubmitStampsFinishOnTerminalEvent(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 1)}
	m := New(agent.New(backend, agent.Config{}))
	m.editor.Focus()
	fixed := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	setConversationInput(m, "hello")
	_, _ = m.submit()
	run := m.op.run
	if run == 0 {
		t.Fatal("submit did not start a run")
	}

	m.applyEvent(agent.Event{Kind: agent.EventDone})

	got, ok := m.runFinished[run]
	if !ok || !got.Equal(fixed) {
		t.Fatalf("runFinished[%v] = %v, %v; want %v", run, got, ok, fixed)
	}
}

// TestNothingStampedBeforeTerminalEvent is Review Focus 1: a non-terminal
// event mid-stream must never record a finish instant.
func TestNothingStampedBeforeTerminalEvent(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 1)}
	m := New(agent.New(backend, agent.Config{}))
	m.editor.Focus()
	setConversationInput(m, "hello")
	_, _ = m.submit()
	if m.op.run == 0 {
		t.Fatal("submit did not start a run")
	}

	streaming := agent.State{
		Version: m.state.Version + 1,
		Blocks: []agent.Block{{
			Role:     assistant.RoleAssistant,
			Kind:     assistant.KindText,
			Complete: false,
			Markdown: &assistant.MarkdownPayload{Content: "thinking"},
		}},
	}
	m.applyEvent(agent.Event{Kind: agent.EventState, State: streaming})

	if len(m.runFinished) != 0 {
		t.Fatalf("runFinished = %v, want none before the terminal event", m.runFinished)
	}
}

// TestResumePendingToolsStampsNothing is Review Focus 2: resumePendingTools
// never sets operation.submitted, so its terminal event stamps nothing even
// though it drives an opTurn run exactly like a submitted one.
func TestResumePendingToolsStampsNothing(t *testing.T) {
	f, id := pushQuestions(t)
	m := newResumedQuestionModel(t, f, id, true)
	waitQuestions(t, m)
	if m.op.run == 0 || m.op.submitted {
		t.Fatalf("op = %+v, want a resumed run that is not submitted", m.op)
	}

	fixed := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	m.applyEvent(agent.Event{Kind: agent.EventDone})

	if len(m.runFinished) != 0 {
		t.Fatalf("runFinished = %v, want none for a resumed (unsubmitted) run", m.runFinished)
	}
}

// TestCancelledResponseIsStamped confirms a cancelled submitted turn is
// stamped too: the line reports when output stopped, not just success.
func TestCancelledResponseIsStamped(t *testing.T) {
	backend := &acceptingBackend{requests: make(chan assistant.SendOptions, 1)}
	m := New(agent.New(backend, agent.Config{}))
	m.editor.Focus()
	fixed := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	setConversationInput(m, "hello")
	_, _ = m.submit()
	run := m.op.run
	if run == 0 {
		t.Fatal("submit did not start a run")
	}

	m.applyEvent(agent.Event{Kind: agent.EventCanceled})

	got, ok := m.runFinished[run]
	if !ok || !got.Equal(fixed) {
		t.Fatalf("runFinished[%v] = %v, %v; want %v", run, got, ok, fixed)
	}
}

// TestResponseTimestampsAnchorsToLastBlockOfRun confirms a run anchors to the
// last block it touched, not the first, and results come back in block
// order rather than RunID order: run 2's blocks sit before run 1's, so an
// implementation that sorted by RunID (or dropped the sort entirely and
// relied on map iteration) would emit these in the wrong sequence.
func TestResponseTimestampsAnchorsToLastBlockOfRun(t *testing.T) {
	t1 := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 26, 15, 13, 0, 0, time.UTC)
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 2},
		{ID: agent.BlockID{Key: "1"}, Run: 2},
		{ID: agent.BlockID{Key: "2"}, Run: 1},
	}
	m := &Model{
		state:       agent.State{Blocks: blocks},
		runFinished: map[agent.RunID]time.Time{1: t1, 2: t2},
	}

	got := m.responseTimestamps()
	want := []responseTimestamp{
		{After: blocks[1].ID, At: t2},
		{After: blocks[2].ID, At: t1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("responseTimestamps() = %+v, want %+v", got, want)
	}
}

// TestResponseTimestampsSkipsRestoredHistory confirms blocks restored from
// history (Run == 0) never anchor a "Done" line.
func TestResponseTimestampsSkipsRestoredHistory(t *testing.T) {
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 0},
		{ID: agent.BlockID{Key: "1"}, Run: 0},
	}
	m := &Model{
		state:       agent.State{Blocks: blocks},
		runFinished: map[agent.RunID]time.Time{},
	}

	got := m.responseTimestamps()
	if len(got) != 0 {
		t.Fatalf("responseTimestamps() = %+v, want none", got)
	}
}

// TestResponseTimestampsOmitsRunWithNoSurvivingBlock is Review Focus 3: a run
// in runFinished with no block in m.state.Blocks must be dropped entirely,
// never anchored to a zero BlockID, which would render before every block.
func TestResponseTimestampsOmitsRunWithNoSurvivingBlock(t *testing.T) {
	fixed := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	m := &Model{
		state:       agent.State{},
		runFinished: map[agent.RunID]time.Time{7: fixed},
	}

	got := m.responseTimestamps()
	if len(got) != 0 {
		t.Fatalf("responseTimestamps() = %+v, want none", got)
	}
}

// TestResponseTimestampsSkipsStillStreamingRun confirms a block whose Run has
// no entry in runFinished yet — the common case of a response still
// streaming — is skipped rather than anchored with a zero time.Time. Without
// the untracked-run guard, m.runFinished[run] would silently return the zero
// value and a live response would show "Done · Jan 1, year 1".
func TestResponseTimestampsSkipsStillStreamingRun(t *testing.T) {
	t1 := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 1},
		{ID: agent.BlockID{Key: "1"}, Run: 1},
		{ID: agent.BlockID{Key: "2"}, Run: 2}, // run 2 is still streaming: no runFinished entry.
	}
	m := &Model{
		state:       agent.State{Blocks: blocks},
		runFinished: map[agent.RunID]time.Time{1: t1},
	}

	got := m.responseTimestamps()
	want := []responseTimestamp{{After: blocks[1].ID, At: t1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("responseTimestamps() = %+v, want %+v", got, want)
	}
}

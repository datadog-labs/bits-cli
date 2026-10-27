package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
)

// TestTranscriptNoticesIncludesDoneLines confirms a finished run produces a
// "Done · <time>" notice anchored to the last block that run touched.
func TestTranscriptNoticesIncludesDoneLines(t *testing.T) {
	at := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 1},
		{ID: agent.BlockID{Key: "1"}, Run: 1},
	}
	m := &Model{
		state:       agent.State{Blocks: blocks},
		runFinished: map[agent.RunID]time.Time{1: at},
	}

	notices := m.transcriptNotices()
	if len(notices) != 1 {
		t.Fatalf("transcriptNotices() = %+v, want exactly one item", notices)
	}
	got := notices[0]
	if got.After != blocks[1].ID {
		t.Fatalf("After = %+v, want %+v", got.After, blocks[1].ID)
	}
	if got.Notice.Level != chat.NoticeDone {
		t.Fatalf("Level = %v, want NoticeDone", got.Notice.Level)
	}
	if want := "Done · " + chat.FormatFinishedAt(at); got.Notice.Text != want {
		t.Fatalf("Text = %q, want %q", got.Notice.Text, want)
	}
}

// TestDoneLinesOrderedWithForkMarkerAndNotices is Review Focus 5: a fork
// marker, two done lines and a session notice anchored across the
// transcript must come back in non-decreasing block order, since
// buildPresentationWithNotices silently re-anchors anything out of order.
func TestDoneLinesOrderedWithForkMarkerAndNotices(t *testing.T) {
	t1 := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 26, 15, 30, 0, 0, time.UTC)
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 1}, // fork marker anchors here
		{ID: agent.BlockID{Key: "1"}, Run: 1}, // run 1's last block: done line 1
		{ID: agent.BlockID{Key: "2"}, Run: 2}, // session notice anchors here
		{ID: agent.BlockID{Key: "3"}, Run: 2}, // run 2's last block: done line 2
	}
	m := &Model{
		state: agent.State{
			Blocks: blocks,
			Fork:   agent.ForkBoundary{After: blocks[0].ID, SourceID: "source-id"},
		},
		notices: []chat.NoticeItem{
			{ID: 1, After: blocks[2].ID, Notice: chat.Notice{Level: chat.NoticeWarn, Text: "warn"}},
		},
		runFinished: map[agent.RunID]time.Time{1: t1, 2: t2},
	}

	notices := m.transcriptNotices()
	if len(notices) != 4 {
		t.Fatalf("transcriptNotices() = %+v, want 4 items", notices)
	}

	index := make(map[agent.BlockID]int, len(blocks))
	for i, b := range blocks {
		index[b.ID] = i
	}
	last := -1
	for _, n := range notices {
		idx, ok := index[n.After]
		if !ok {
			t.Fatalf("notice anchored to unknown block: %+v", n)
		}
		if idx < last {
			t.Fatalf("notices out of block order: %+v", notices)
		}
		last = idx
	}

	levels := make([]chat.NoticeLevel, len(notices))
	for i, n := range notices {
		levels[i] = n.Notice.Level
	}
	want := []chat.NoticeLevel{chat.NoticeFork, chat.NoticeDone, chat.NoticeWarn, chat.NoticeDone}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("levels = %+v, want %+v", levels, want)
		}
	}
}

// TestNoDoneLineWhileStreaming confirms a still-streaming response (its run
// has no entry in runFinished yet) never produces a NoticeDone item.
func TestNoDoneLineWhileStreaming(t *testing.T) {
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 1},
	}
	m := &Model{
		state:       agent.State{Blocks: blocks},
		runFinished: map[agent.RunID]time.Time{},
	}

	notices := m.transcriptNotices()
	for _, n := range notices {
		if n.Notice.Level == chat.NoticeDone {
			t.Fatalf("transcriptNotices() = %+v, want no NoticeDone item while streaming", notices)
		}
	}
}

// TestDoneLineWhileLaterRunStreams pins the case TestNoDoneLineWhileStreaming
// could not: an earlier run has finished while a later one is still
// streaming (no runFinished entry yet). Exactly one NoticeDone item must
// appear, anchored to run 1's last block — not to the overall last block,
// which belongs to the still-streaming run 2.
func TestDoneLineWhileLaterRunStreams(t *testing.T) {
	t1 := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 1},
		{ID: agent.BlockID{Key: "1"}, Run: 1}, // run 1's last block: expected anchor
		{ID: agent.BlockID{Key: "2"}, Run: 2}, // run 2 still streaming: no done line
	}
	m := &Model{
		state:       agent.State{Blocks: blocks},
		runFinished: map[agent.RunID]time.Time{1: t1},
	}

	notices := m.transcriptNotices()
	var done []chat.NoticeItem
	for _, n := range notices {
		if n.Notice.Level == chat.NoticeDone {
			done = append(done, n)
		}
	}
	if len(done) != 1 {
		t.Fatalf("transcriptNotices() done lines = %+v, want exactly one", done)
	}
	if done[0].After != blocks[1].ID {
		t.Fatalf("done line After = %+v, want %+v (run 1's last block, not run 2's)", done[0].After, blocks[1].ID)
	}
}

// TestDoneLineSortsBeforeSameAnchorNotice confirms that when a done line and
// a session notice share an anchor — the common case right after a response
// finishes and /copy posts "Copied to clipboard." to the same last block —
// the done line sorts first, so it stays directly under the response it
// belongs to instead of drifting below a later notice.
func TestDoneLineSortsBeforeSameAnchorNotice(t *testing.T) {
	at := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 1},
	}
	m := &Model{
		state: agent.State{Blocks: blocks},
		notices: []chat.NoticeItem{
			{ID: 1, After: blocks[0].ID, Notice: chat.Notice{Level: chat.NoticeInfo, Text: "Copied to clipboard."}},
		},
		runFinished: map[agent.RunID]time.Time{1: at},
	}

	notices := m.transcriptNotices()
	if len(notices) != 2 {
		t.Fatalf("transcriptNotices() = %+v, want 2 items", notices)
	}
	if notices[0].Notice.Level != chat.NoticeDone || notices[1].Notice.Level != chat.NoticeInfo {
		t.Fatalf("notices = %+v, want [NoticeDone, NoticeInfo] on a tied anchor", notices)
	}
}

// TestDoneMarkerReachesRenderedViewOnTerminalEvent is the seam-level
// regression every test above missed by asserting transcriptNotices() or
// renderNotice() in isolation: it drives a real submitted run through the
// engine up to its terminal event, folds that event through applyRunEvent
// exactly as Update's normal dispatch would, and checks that the "✦ Done"
// marker actually reaches the rendered view — not just the notice list that
// feeds it.
func TestDoneMarkerReachesRenderedViewOnTerminalEvent(t *testing.T) {
	m := New(agent.New(&streamingConversationBackend{}, agent.Config{}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.relayout()
	m.editor.Focus()
	fixed := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }

	setConversationInput(m, "hello")
	_, _ = m.submit()
	run := m.op.run
	if run == 0 {
		t.Fatal("submit did not start a run")
	}

	// Drive every event up to, but not including, the run's terminal event:
	// this mirrors the real engine's sequencing (a final EventState, then
	// EventDone last) without letting Update's normal routing apply the
	// terminal event for us.
	for {
		msg := runConversationCmd(t, waitEvent(m.events))
		ev, ok := msg.(engineEventMsg)
		if !ok {
			t.Fatalf("unexpected message: %#v", msg)
		}
		if ev.ev.Terminal() {
			break
		}
		m.Update(msg)
	}
	if len(m.state.Blocks) != 2 || !m.state.Blocks[1].Complete {
		t.Fatalf("blocks before terminal event = %+v, want a completed assistant response", m.state.Blocks)
	}

	// Fold the terminal event directly, exactly as applyRunEvent does for any
	// EventDone arriving through Update's normal dispatch.
	m.applyRunEvent(agent.Event{Kind: agent.EventDone})

	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "✦") {
		t.Fatalf("rendered view = %q, want a \"✦ Done · ...\" line", got)
	}
}

// TestDoneMarkerReachesRenderedViewOnCancel is the EventCanceled counterpart
// of TestDoneMarkerReachesRenderedViewOnTerminalEvent: Esc ends a turn with
// EventCanceled rather than EventDone, and that path must resync the
// transcript too.
func TestDoneMarkerReachesRenderedViewOnCancel(t *testing.T) {
	m := New(agent.New(&streamingConversationBackend{}, agent.Config{}))
	m.mode = ModeChat
	m.resize(80, 24)
	m.relayout()
	m.editor.Focus()
	fixed := time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }

	setConversationInput(m, "hello")
	_, _ = m.submit()
	run := m.op.run
	if run == 0 {
		t.Fatal("submit did not start a run")
	}

	for {
		msg := runConversationCmd(t, waitEvent(m.events))
		ev, ok := msg.(engineEventMsg)
		if !ok {
			t.Fatalf("unexpected message: %#v", msg)
		}
		if ev.ev.Terminal() {
			break
		}
		m.Update(msg)
	}

	m.applyRunEvent(agent.Event{Kind: agent.EventCanceled})

	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "✦") {
		t.Fatalf("rendered view = %q, want a \"✦ Done · ...\" line after cancel", got)
	}
}

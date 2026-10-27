package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	conversationview "github.com/datadog-labs/bits-cli/internal/tui/conversations"
)

// forkButtonTestModel builds a chat model with one finished response, whose
// done line is eligible for the fork button: a valid conversation, no busy
// operation, no running conversation task, no open picker.
func forkButtonTestModel(t *testing.T) (*Model, *tuiForkBackend) {
	t.Helper()
	b := &tuiForkBackend{resumeBackend: newResumeBackend()}
	e := agent.New(b, agent.Config{ConversationID: forkSourceID})
	m := New(e)
	m.setMode(ModeChat)
	m.resize(80, 16)
	m.relayout()
	m.state.Blocks = []agent.Block{{
		ID: agent.BlockID{Scope: agent.ScopeMessage, Key: "response"}, Role: assistant.RoleAssistant, Kind: assistant.KindText,
		Markdown: &assistant.MarkdownPayload{Content: "the answer"}, Run: 1,
	}}
	m.runFinished = map[agent.RunID]time.Time{1: time.Date(2026, 10, 26, 15, 12, 0, 0, time.UTC)}
	m.syncTranscript()
	m.list.ScrollToBottom()
	t.Cleanup(m.Detach)
	return m, b
}

// forkButtonCell locates the button's middle-row glyph cell in the model's
// current rendered view — the real on-screen position, not a recomputed
// span. The transcript renders flush at (0,0), so these are also the mouse
// coordinates Update expects.
func forkButtonCell(t *testing.T, m *Model) (x, y int) {
	t.Helper()
	rows := strings.Split(ansi.Strip(m.View().Content), "\n")
	for row, line := range rows {
		if idx := strings.Index(line, "⑂"); idx >= 0 {
			return ansi.StringWidth(line[:idx]), row
		}
	}
	t.Fatalf("no fork button in view:\n%s", strings.Join(rows, "\n"))
	return 0, 0
}

func TestForkButtonClickAndReleaseFiresFork(t *testing.T) {
	m, _ := forkButtonTestModel(t)
	x, y := forkButtonCell(t, m)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if !m.forkButton.pressed || m.selection.selecting() {
		t.Fatalf("press did not record pressed state or started a selection: pressed=%v selecting=%v", m.forkButton.pressed, m.selection.selecting())
	}
	if m.op.busy() {
		t.Fatal("press alone started the fork")
	}

	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
	if cmd == nil || m.op.kind != opFork || !m.op.busy() {
		t.Fatal("release on the same cell did not fire the fork")
	}
	// The backend call itself is covered by TestForkSwitchesInPlace.
}

// TestForkButtonOneColumnOutsideMisses is the boundary case
// TestForkButtonMissColumnDoesNotFireAndStartsSelection's x-5 can't pin: the
// very next cell past the button's edge must still miss and fall through to
// selection, at both the left and right edges.
func TestForkButtonOneColumnOutsideMisses(t *testing.T) {
	m, b := forkButtonTestModel(t)
	x, y := forkButtonCell(t, m)
	// forkButtonCell returns the glyph column; the button renders as
	// "│ ⑂ fork │", so its left border sits two cells left of the glyph and its
	// right edge eight to the right — one past either edge is x-3 and x+8.
	for _, miss := range []int{x - 3, x + 8} {
		m.selection.clear()
		m.forkButton.pressed = false
		m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: miss, Y: y})
		if m.forkButton.pressed {
			t.Fatalf("column %d (one outside the button) recorded a press", miss)
		}
		if !m.selection.selecting() {
			t.Fatalf("column %d (one outside the button) did not begin a selection", miss)
		}
		m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: miss, Y: y})
		if m.op.busy() || b.calls != 0 {
			t.Fatalf("column %d fired the fork", miss)
		}
	}
}

// TestForkButtonHoverRepaints confirms motion over the button changes its
// painted colors without shifting any cell — the repaint goes through the
// per-Update reconcileForkButton pass (see its doc comment for why this
// isn't a dedicated MouseMotionMsg case), not a bespoke motion handler.
func TestForkButtonHoverRepaints(t *testing.T) {
	m, _ := forkButtonTestModel(t)
	x, y := forkButtonCell(t, m)
	rest := strings.Split(m.View().Content, "\n")[y]

	m.Update(tea.MouseMotionMsg{X: x, Y: y})
	hover := strings.Split(m.View().Content, "\n")[y]
	if hover == rest {
		t.Error("motion over the button did not repaint it active")
	}
	// Hover must not shift the row: same cells, different colors only.
	if ansi.Strip(hover) != ansi.Strip(rest) {
		t.Errorf("hover changed layout:\n%q\n%q", ansi.Strip(rest), ansi.Strip(hover))
	}
	m.Update(tea.MouseMotionMsg{X: 0, Y: y})
	if away := strings.Split(m.View().Content, "\n")[y]; away != rest {
		t.Error("moving off the button did not restore the resting paint")
	}
}

// TestForkButtonMouseIgnoredOutsideChatMode pins the critical guard: the
// button must never fire, nor swallow a click meant for another surface,
// anywhere but ModeChat — the transcript isn't even drawn in ModeStatus, and
// ModePermissions deliberately swallows chat pointer input itself.
func TestForkButtonMouseIgnoredOutsideChatMode(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
	}{
		{"chat", ModeChat},
		{"status", ModeStatus},
		{"permissions", ModePermissions},
		{"conversations", ModeConversations},
	}
	for _, c := range cases {
		mode := c.mode
		t.Run(c.name, func(t *testing.T) {
			m, b := forkButtonTestModel(t)
			x, y := forkButtonCell(t, m)
			m.mode = mode

			m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})

			if mode == ModeChat {
				// The backend call itself only happens once the returned
				// command runs; op.kind flips synchronously in dispatch,
				// same as TestForkButtonClickAndReleaseFiresFork.
				if !m.op.busy() || m.op.kind != opFork {
					t.Fatalf("setup: ModeChat should still fire (op=%+v)", m.op)
				}
				return
			}
			if m.op.busy() || b.calls != 0 {
				t.Fatalf("mode %v: fork fired outside chat", mode)
			}
			if m.forkButton.pressed {
				t.Fatalf("mode %v: press recorded outside chat", mode)
			}
		})
	}
}

func TestForkButtonMissColumnDoesNotFireAndStartsSelection(t *testing.T) {
	m, b := forkButtonTestModel(t)
	x, y := forkButtonCell(t, m)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x - 5, Y: y})
	if m.forkButton.pressed {
		t.Fatal("a click outside the button recorded a press")
	}
	if !m.selection.selecting() {
		t.Fatal("a non-button click did not begin a selection")
	}
	m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x - 5, Y: y})
	if m.op.busy() || b.calls != 0 {
		t.Fatal("a miss click fired the fork")
	}
}

func TestForkButtonReleaseAwayDoesNotFire(t *testing.T) {
	m, b := forkButtonTestModel(t)
	x, y := forkButtonCell(t, m)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if !m.forkButton.pressed {
		t.Fatal("setup: press did not record")
	}
	// Release somewhere else entirely — not just outside the button, but off
	// the row it sits on.
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 0, Y: 0})
	if cmd != nil || m.op.busy() || b.calls != 0 {
		t.Fatal("release away from the press fired the fork")
	}
	if m.forkButton.pressed {
		t.Fatal("release did not clear the pressed state")
	}
}

// TestForkButtonPressClearsWhenIneligible pins the constraint that a press
// must not survive the button becoming ineligible mid-gesture — a busy
// operation starting, in this case — exactly as followOverlay clears its own
// pressed/hover when its control disappears.
func TestForkButtonPressClearsWhenIneligible(t *testing.T) {
	m, _ := forkButtonTestModel(t)
	x, y := forkButtonCell(t, m)

	m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	if !m.forkButton.pressed {
		t.Fatal("setup: press did not record")
	}

	m.op.kind = opRestore // some other operation starts mid-gesture
	m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	if m.forkButton.pressed {
		t.Fatal("press survived the operation becoming busy")
	}

	// A release landing back on the same cell must not fire now either.
	m.op.kind = opIdle
	_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
	if cmd != nil {
		t.Fatal("release fired after the cleared press")
	}
}

// TestActionFlagOnLastDoneLineOnly confirms transcriptNotices sets Action on
// the transcript's last done line and no other — a stable fact about
// position in the transcript, independent of whether a fork could actually
// start right now. (Eligibility is a separate, volatile question: see
// TestForkButtonPressReleaseBlockedWhenIneligible, which re-checks it live
// at press and release instead of baking it into the render.)
func TestActionFlagOnLastDoneLineOnly(t *testing.T) {
	t1 := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 26, 15, 30, 0, 0, time.UTC)
	blocks := []agent.Block{
		{ID: agent.BlockID{Key: "0"}, Run: 1},
		{ID: agent.BlockID{Key: "1"}, Run: 2},
	}
	base := func() *Model {
		return &Model{
			state:       agent.State{Blocks: blocks, ConversationID: forkSourceID},
			runFinished: map[agent.RunID]time.Time{1: t1, 2: t2},
		}
	}

	notices := base().transcriptNotices()
	var actions []bool
	for _, n := range notices {
		actions = append(actions, n.Notice.Action)
	}
	if len(actions) != 2 || actions[0] || !actions[1] {
		t.Fatalf("Action flags = %+v, want [false, true]", actions)
	}

	// The flag must stay true on the last done line even while a fork is
	// not currently possible: the button renders inert rather than
	// disappearing, and a click is blocked by forkEligible at release, not
	// by the button's absence.
	for _, kind := range []string{"busy", "conversationTask", "picker", "invalidID"} {
		t.Run(kind, func(t *testing.T) {
			m := base()
			switch kind {
			case "busy":
				m.op.kind = opRestore
			case "conversationTask":
				m.conversationTask.cancel = func() {}
			case "picker":
				m.picker = &conversationview.Model{}
			case "invalidID":
				m.state.ConversationID = ""
			}
			notices := m.transcriptNotices()
			if len(notices) != 2 || notices[0].Notice.Action || !notices[1].Notice.Action {
				t.Fatalf("Action flags changed while ineligible (%s): %+v", kind, notices)
			}
		})
	}
}

// TestForkButtonPressReleaseBlockedWhenIneligible is where the four guards
// TestActionFlagOnLastDoneLineOnly used to table now live: the button still
// renders on the last done line (Action no longer encodes eligibility), but
// a press-then-release on it must not start a fork while any of startFork's
// guards would reject it.
func TestForkButtonPressReleaseBlockedWhenIneligible(t *testing.T) {
	for _, kind := range []string{"busy", "conversationTask", "picker", "invalidID"} {
		t.Run(kind, func(t *testing.T) {
			m, b := forkButtonTestModel(t)
			x, y := forkButtonCell(t, m)

			switch kind {
			case "busy":
				m.op.kind = opRestore
			case "conversationTask":
				m.conversationTask.cancel = func() {}
			case "picker":
				m.picker = &conversationview.Model{}
			case "invalidID":
				m.state.ConversationID = ""
			}

			m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
			_, cmd := m.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: x, Y: y})
			if cmd != nil || m.op.kind == opFork || b.calls != 0 {
				t.Fatalf("%s: press+release fired a fork while ineligible", kind)
			}
		})
	}
}

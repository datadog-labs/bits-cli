package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// fixedNow keeps relative timestamps deterministic.
func fixedNow() time.Time { return time.Unix(1_700_000_000, 0) }

func resumeFixture(count int) *resume {
	summaries := make([]assistant.ConversationSummary, count)
	for i := range summaries {
		// Ids must be distinct and pass validConversationID, or a selection test
		// passes for the wrong reason.
		id := fmt.Sprintf("%08d-1234-1234-1234-123456781234", i)
		summaries[i] = assistant.ConversationSummary{
			ID:             id,
			ConversationID: id,
			Title:          strings.Repeat("title ", 20),
			UpdatedAt:      fixedNow().Add(-time.Duration(i+1) * time.Hour).UnixMilli(),
		}
	}
	r := &resume{now: fixedNow}
	r.setConversations(summaries)
	return r
}

// The header reserves resumeBlockHeight(visible) rows; overflowing it pushes
// content past the reservation, which is the PR #112 bug class.
func TestResumeBlockHeightMatchesItsFormula(t *testing.T) {
	theme := styles.Default(true)
	for _, count := range []int{1, 4, 32} {
		r := resumeFixture(count)
		for visible := 1; visible <= resumeRows; visible++ {
			if visible > count {
				continue
			}
			got := lipgloss.Height(r.view(theme, 80, visible))
			if want := resumeBlockHeight(visible); got != want {
				t.Fatalf("count=%d visible=%d: %d rows, want %d", count, visible, got, want)
			}
		}
	}
}

func TestResumeBlockNeverExceedsItsWidth(t *testing.T) {
	theme := styles.Default(true)
	r := resumeFixture(32)
	for width := minimumChatWidth; width <= 200; width++ {
		for i, line := range strings.Split(r.view(theme, width, resumeRows), "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Fatalf("width=%d line %d measures %d columns", width, i, got)
			}
		}
	}
}

// Selection clamps rather than wrapping, and the window follows it.
func TestResumeSelectionClampsAndScrolls(t *testing.T) {
	r := resumeFixture(32)
	r.move(-1, resumeRows)
	if r.selected != 0 || r.top != 0 {
		t.Fatalf("up from the first row moved to selected=%d top=%d", r.selected, r.top)
	}
	for range 10 {
		r.move(1, resumeRows)
	}
	if r.selected != 10 {
		t.Fatalf("selected = %d, want 10", r.selected)
	}
	if r.top != 10-resumeRows+1 {
		t.Fatalf("top = %d, want the window to follow the selection", r.top)
	}
	for range 100 {
		r.move(1, resumeRows)
	}
	if r.selected != 31 {
		t.Fatalf("selected = %d, want the last row", r.selected)
	}
}

func TestResumeIndicatorCountsRemainingRows(t *testing.T) {
	theme := styles.Default(true)
	r := resumeFixture(32)
	if got := r.view(theme, 80, resumeRows); !strings.Contains(got, "28 more below") {
		t.Fatalf("missing the below indicator:\n%s", got)
	}
	for range 31 {
		r.move(1, resumeRows)
	}
	out := r.view(theme, 80, resumeRows)
	if !strings.Contains(out, "more above") {
		t.Fatalf("missing the above indicator when scrolled to the end:\n%s", out)
	}
	// Height must not change between the two, or the reservation breaks.
	if lipgloss.Height(out) != resumeBlockHeight(resumeRows) {
		t.Fatalf("scrolled block is %d rows", lipgloss.Height(out))
	}
}

// A list that fits entirely still reserves the indicator row, so no reflow
// happens when the user scrolls.
func TestResumeReservesTheIndicatorRowWhenNothingIsHidden(t *testing.T) {
	theme := styles.Default(true)
	r := resumeFixture(2)
	out := r.view(theme, 80, 2)
	if strings.Contains(out, "more below") || strings.Contains(out, "more above") {
		t.Fatalf("indicator shown with nothing hidden:\n%s", out)
	}
	if got := lipgloss.Height(out); got != resumeBlockHeight(2) {
		t.Fatalf("block is %d rows, want %d", got, resumeBlockHeight(2))
	}
}

func TestResumeSelectedIDTracksSelection(t *testing.T) {
	r := resumeFixture(4)
	id, ok := r.selectedID()
	if !ok || id != r.conversations[0].ConversationID {
		t.Fatalf("selectedID = (%q, %v)", id, ok)
	}
	r.move(1, resumeRows)
	if id, _ := r.selectedID(); id != r.conversations[1].ConversationID {
		t.Fatalf("selectedID did not follow the selection: %q", id)
	}
	empty := &resume{now: fixedNow}
	if _, ok := empty.selectedID(); ok {
		t.Fatal("an empty list reported a selection")
	}
}

func TestResumeResetReturnsToTheFirstRow(t *testing.T) {
	r := resumeFixture(32)
	for range 10 {
		r.move(1, resumeRows)
	}
	r.reset()
	if r.selected != 0 || r.top != 0 {
		t.Fatalf("reset left selected=%d top=%d", r.selected, r.top)
	}
}

// resume.view must return "" when there are no conversations: the caller
// gates this away before calling view, but the unit itself must not depend
// on the caller staying honest, since an empty view would otherwise emit a
// 4-line block while resumeBlockHeight(0) implies 3.
func TestResumeViewIsEmptyWithoutConversations(t *testing.T) {
	theme := styles.Default(true)
	r := &resume{now: fixedNow}
	if got := r.view(theme, 80, resumeRows); got != "" {
		t.Fatalf("view() = %q, want empty string for no conversations", got)
	}
}

// resumeKeyModel builds a chat-mode model with the startup offer populated
// and visible, ready to exercise handleEditorKey against it. newShell leaves
// mode at its zero value (ModeTermInit), on which layoutTranscript
// early-returns, so mode is forced to ModeChat here.
func resumeKeyModel(t *testing.T) *Model {
	t.Helper()
	m := welcomeModel(120, 40)
	m.mode = ModeChat
	m.engine = agent.New(fake.New(), assistant.SendOptions{})
	m.resume = *resumeFixture(32)
	m.layoutTranscript()
	return m
}

// Arrows browse the offer while it is visible.
func TestArrowKeysBrowseTheOffer(t *testing.T) {
	m := resumeKeyModel(t)
	m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.resume.selected != 1 {
		t.Fatalf("down selected row %d", m.resume.selected)
	}
	m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.resume.selected != 0 {
		t.Fatalf("up selected row %d", m.resume.selected)
	}
}

// With the offer hidden, arrows belong to the composer again.
func TestArrowKeysIgnoredWhenTheOfferIsHidden(t *testing.T) {
	m := resumeKeyModel(t)
	m.editor.SetValue("draft")
	m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.resume.selected != 0 {
		t.Fatalf("arrows moved the hidden offer to row %d", m.resume.selected)
	}
}

// Enter resumes the selected conversation rather than submitting.
func TestEnterResumesTheSelectedConversation(t *testing.T) {
	m := resumeKeyModel(t)
	m.resume.move(1, resumeRows)
	want, _ := m.resume.selectedID()
	_, cmd := m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if m.mode != ModeConversations || m.picker == nil {
		t.Fatalf("mode = %v, picker = %v", m.mode, m.picker)
	}
	if m.conversationSwitchID != want {
		t.Fatalf("switching to %q, want %q", m.conversationSwitchID, want)
	}
}

// pgup/pgdown stay transcript scrolling, not offer navigation.
func TestPageKeysDoNotMoveTheOffer(t *testing.T) {
	m := resumeKeyModel(t)
	m.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.resume.selected != 0 {
		t.Fatalf("pgdown moved the offer to row %d", m.resume.selected)
	}
}

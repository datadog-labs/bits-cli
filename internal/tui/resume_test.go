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
	"github.com/DataDog/bits-cli/internal/tui/conversations"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// fixedNow keeps relative timestamps deterministic.
func fixedNow() time.Time { return time.Unix(1_700_000_000, 0) }

func resumeFixture(count int) *resume {
	summaries := make([]assistant.ConversationSummary, count)
	for i := range summaries {
		// Ids must be distinct and pass ValidConversationID, or a selection test
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

// The window is derived from the selection at render time. Storing it as an
// independent field and reconciling it only in move() let a later, larger
// visible count walk past the end of the slice.
func TestResumeViewSurvivesAGrowingWindow(t *testing.T) {
	theme := styles.Default(true)
	r := resumeFixture(32)
	// Scroll to the end while the terminal is short enough to show one row.
	for range 40 {
		r.move(1, 1)
	}
	out := r.view(theme, 80, resumeRows)
	if got := lipgloss.Height(out); got != resumeBlockHeight(resumeRows) {
		t.Fatalf("grown window rendered %d rows, want %d", got, resumeBlockHeight(resumeRows))
	}
	if want := "28 more above"; !strings.Contains(ansi.Strip(out), want) {
		t.Fatalf("indicator does not report %q after the window grew:\n%s", want, ansi.Strip(out))
	}
	if strings.Contains(ansi.Strip(out), "more below") {
		t.Fatalf("indicator claims rows below while pinned to the end:\n%s", ansi.Strip(out))
	}
}

// selectedRow returns the rendered row carrying the selection marker, so a
// test can tell which conversation the user is looking at without reaching
// into the window arithmetic under test.
func selectedRow(t *testing.T, theme styles.Theme, out string) string {
	t.Helper()
	marker := strings.TrimSpace(theme.Selector.SelectedMarker)
	for _, line := range strings.Split(ansi.Strip(out), "\n") {
		if strings.Contains(line, marker) {
			return strings.TrimRight(line, " ")
		}
	}
	return ""
}

// Shrinking the terminal must pull the window onto the selection, or the block
// renders rows the user did not choose while enter resumes an off-screen one.
func TestResumeSelectionStaysInsideTheRenderedWindow(t *testing.T) {
	theme := styles.Default(true)
	r := resumeFixture(32)
	for range 3 {
		r.move(1, resumeRows)
	}
	if r.selected != 3 {
		t.Fatalf("selected = %d, want 3", r.selected)
	}
	want := " " + conversations.RelativeUpdatedAt(r.conversations[r.selected].UpdatedAt, fixedNow())
	out := r.view(theme, 80, 1)
	if got := selectedRow(t, theme, out); !strings.HasSuffix(got, want) {
		t.Fatalf("one-row window shows %q, want the selected row ending %q:\n%s", got, want, ansi.Strip(out))
	}
}

// setConversations applies the picker's normalisation, so the offer's first
// rows are the genuinely most recent ones whatever order the API returned.
func TestResumeSortsConversationsNewestFirst(t *testing.T) {
	summaries := resumeFixture(8).conversations
	shuffled := []assistant.ConversationSummary{
		summaries[5], summaries[0], summaries[7], summaries[2],
		summaries[6], summaries[1], summaries[4], summaries[3],
	}
	r := &resume{now: fixedNow}
	r.setConversations(shuffled)
	for i := range r.conversations {
		if got, want := r.conversations[i].ConversationID, summaries[i].ConversationID; got != want {
			t.Fatalf("row %d is %q, want %q: the offer is not sorted newest first", i, got, want)
		}
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
// and visible, ready to exercise handleEditorKey against it.
func resumeKeyModel(t *testing.T) *Model {
	t.Helper()
	m := welcomeModel(120, 40)
	m.engine = agent.New(fake.New(), assistant.SendOptions{})
	m.resume = *resumeFixture(32)
	m.relayout()
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

// Once the transcript has prompts the offer is gone and Up recalls the newest
// one, wherever the editor owns input.
func TestArrowKeysRecallPromptsOutsideTheOffer(t *testing.T) {
	user := func(text string) agent.Block {
		return agent.Block{Role: assistant.RoleUser, Kind: assistant.KindText, Markdown: &assistant.MarkdownPayload{Content: text}}
	}
	for _, test := range []struct {
		name  string
		setup func(*Model)
		want  string
	}{
		{name: "idle", want: "second"},
		{name: "a running turn", setup: func(m *Model) { m.op = operation{kind: opTurn, events: make(chan agent.Event)} }, want: "second"},
		{name: "a pending approval owns the keys", setup: func(m *Model) {
			m.approval.pending = []agent.Block{animToolBlock(agent.ToolAwaitingApproval)}
		}, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := resumeKeyModel(t)
			m.transcript.Blocks = []agent.Block{user("first"), user("second")}
			m.syncTranscript()
			if test.setup != nil {
				test.setup(m)
			}
			m.reconcileFocus() // as every Update does before the user can type
			m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			if got := m.editor.Value(); got != test.want {
				t.Fatalf("editor value = %q, want %q", got, test.want)
			}
		})
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

// The spec requires /new to bring the offer back.
func TestNewConversationBringsTheOfferBack(t *testing.T) {
	m := resumeKeyModel(t)
	m.transcript.Blocks = []agent.Block{{
		ID:       agent.BlockID{Scope: agent.ScopeLocal, Key: "a", Kind: assistant.KindText},
		Kind:     assistant.KindText,
		Complete: true,
		Markdown: &assistant.MarkdownPayload{Content: "hello"},
	}}
	m.syncTranscript()
	m.relayout()
	if strings.Contains(m.list.Render(), resumeTitle) {
		t.Fatal("offer rendered while the transcript had content")
	}

	setConversationInput(m, "/new")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if !strings.Contains(m.list.Render(), resumeTitle) {
		t.Fatalf("the offer did not return after /new:\n%s", m.list.Render())
	}
}

// The focused row's timestamp takes the title's accent, so the whole row reads
// as one selected unit; unfocused rows keep the tertiary level.
func TestSelectedRowTimestampTakesTheTitleAccent(t *testing.T) {
	theme := styles.Default(true)
	r := resumeFixture(4)
	stamp := conversations.RelativeUpdatedAt(r.conversations[0].UpdatedAt, fixedNow())

	accented := theme.Text.Tertiary.Foreground(theme.Selector.Selected.GetForeground()).Render(stamp)
	if got := r.row(theme, 80, 0); !strings.Contains(got, accented) {
		t.Fatalf("focused row does not carry the accented timestamp:\n%q", got)
	}

	r.selected = 1
	if got := r.row(theme, 80, 0); !strings.Contains(got, theme.Text.Tertiary.Render(stamp)) {
		t.Fatalf("unfocused row does not keep the tertiary timestamp:\n%q", got)
	}
}

// An empty composer and an empty transcript both hold for a moment after
// submitting, so the turn is the gate that closes the window.
func TestResumeHiddenWhileATurnIsRunning(t *testing.T) {
	m := welcomeModel(120, 40)
	m.mode = ModeChat
	m.resume = *resumeFixture(4)
	if !m.showResume() {
		t.Fatal("offer not shown while idle")
	}

	events := make(chan agent.Event)
	m.op = operation{kind: opTurn, events: events}
	if m.showResume() {
		t.Fatal("offer shown while a turn was running")
	}
}

func TestUnfocusedRowTitleUsesSecondaryText(t *testing.T) {
	theme := styles.Default(true)
	r := resumeFixture(4)

	focused, unfocused := r.row(theme, 80, 0), r.row(theme, 80, 1)

	if !strings.Contains(unfocused, sgrFor(theme.Text.Secondary)) {
		t.Fatalf("unfocused row is not secondary:\n%q", unfocused)
	}
	if strings.Contains(unfocused, sgrFor(theme.Selector.Selected)) {
		t.Fatalf("unfocused row carries the focused accent:\n%q", unfocused)
	}
	if !strings.Contains(focused, sgrFor(theme.Selector.Selected)) {
		t.Fatalf("focused row lost its accent:\n%q", focused)
	}
}

// sgrFor returns the escape prefix a style emits, so a test can assert which
// token painted a span.
func sgrFor(style lipgloss.Style) string {
	rendered := style.Render("x")
	return strings.SplitN(rendered, "x", 2)[0]
}

package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/conversations"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// resumeRows is how many conversations the offer shows at full height.
const resumeRows = 4

const (
	resumeTitle     = "Resume from a recent conversation with Bits?"
	resumeHint      = " ↑/↓ browse · start typing for a new conversation "
	resumeColumnGap = 2

	// Narrower than this, a title truncates to noise, so the row shows only its
	// timestamp. Eight columns is the narrowest that still leaves a readable
	// word or two beside the ellipsis; below it every row truncates to the same
	// few characters and the list stops distinguishing conversations at all.
	resumeMinTitleWidth = 8
)

// resumeBlockHeight is the block's height for the given row count: a title, a
// blank row, the rows separated by blanks, the scroll indicator, and the hint.
func resumeBlockHeight(rows int) int { return 2*rows + 3 }

// resume is the startup conversation offer. It owns the selection and a scroll
// hint; visibility is the caller's decision.
type resume struct {
	conversations []assistant.ConversationSummary

	selected int

	// top is a hint, not the window. The number of visible rows changes on
	// every resize, so the rendered window is always derived from the selection
	// by window(); top only remembers how far the user had scrolled when the
	// selection alone does not pin it.
	top int

	now func() time.Time
}

// setConversations replaces the offer, returning to the first row. It applies
// the same normalisation as the full picker, so "recent" means the same thing
// in both places whatever order the backend answered in.
func (r *resume) setConversations(summaries []assistant.ConversationSummary) {
	r.conversations = conversations.Ordered(summaries)
	r.reset()
}

func (r *resume) reset() { r.selected, r.top = 0, 0 }

func (r *resume) empty() bool { return len(r.conversations) == 0 }

// move walks the selection by delta, clamping at both ends, and slides the
// scroll hint so the selection stays inside the window.
func (r *resume) move(delta, visible int) {
	if r.empty() || visible < 1 {
		return
	}
	r.selected = min(max(r.selected+delta, 0), len(r.conversations)-1)
	r.top = r.window(visible)
}

// window is the index of the first rendered row for a window of visible rows.
// It is derived rather than stored: visible changes independently of the
// selection (a resize, an approval block docking), so every path that renders
// or measures the window must call this instead of reading r.top. A stored top
// went stale in both directions -- a grown window walked past the end of the
// slice, and a shrunken one left the selection off-screen.
func (r *resume) window(visible int) int {
	top := min(max(r.top, r.selected-visible+1), r.selected)
	return min(max(top, 0), max(0, len(r.conversations)-visible))
}

func (r *resume) selectedID() (string, bool) {
	if r.empty() || r.selected < 0 || r.selected >= len(r.conversations) {
		return "", false
	}
	return r.conversations[r.selected].ConversationID, true
}

// view renders exactly resumeBlockHeight(visible) rows, none wider than
// width, or "" when there are no conversations to offer.
func (r *resume) view(theme styles.Theme, width, visible int) string {
	if r.empty() {
		return ""
	}
	visible = min(max(visible, 1), len(r.conversations))
	top := r.window(visible)
	lines := []string{
		ansi.Truncate(theme.Text.Primary.Bold(true).Render(resumeTitle), width, "…"),
		"",
	}
	for i := top; i < top+visible; i++ {
		if i > top {
			lines = append(lines, "")
		}
		lines = append(lines, r.row(theme, width, i))
	}
	return strings.Join(append(lines,
		r.indicator(theme, width, visible),
		r.hint(theme, width),
	), "\n")
}

// row is a marker and title on the left with the relative timestamp flushed
// right, padded so the whole row measures exactly width.
func (r *resume) row(theme styles.Theme, width, index int) string {
	marker, style := theme.Selector.Marker, theme.Selector.Item
	if index == r.selected {
		marker, style = theme.Selector.SelectedMarker, theme.Selector.Selected
	}
	summary := r.conversations[index]
	stamp := conversations.RelativeUpdatedAt(summary.UpdatedAt, r.now())
	left := ansi.StringWidth(marker)
	titleWidth := width - left - ansi.StringWidth(stamp) - resumeColumnGap

	title := ""
	if titleWidth >= resumeMinTitleWidth {
		// SafeTitle already single-lines its input.
		title = ansi.Truncate(conversations.SafeTitle(summary), titleWidth, "…")
	}
	pad := max(1, width-left-ansi.StringWidth(title)-ansi.StringWidth(stamp))
	row := style.Render(marker+title) + strings.Repeat(" ", pad) + theme.Text.Tertiary.Render(stamp)
	return ansi.Truncate(row, width, "…")
}

// indicator reports what the window hides. The row is always present, blank
// when everything is visible, so scrolling never changes the block's height.
// It derives the window exactly as view does, so the counts can never describe
// a window that was not rendered.
func (r *resume) indicator(theme styles.Theme, width, visible int) string {
	top := r.window(visible)
	switch below, above := len(r.conversations)-top-visible, top; {
	case below > 0:
		return ansi.Truncate(theme.Text.Tertiary.Render(fmt.Sprintf("↓ %d more below", below)), width, "…")
	case above > 0:
		return ansi.Truncate(theme.Text.Tertiary.Render(fmt.Sprintf("↑ %d more above", above)), width, "…")
	default:
		return ""
	}
}

// hint centres the key help between horizontal rules, dropping the rules when
// they would not fit.
func (r *resume) hint(theme styles.Theme, width int) string {
	label := resumeHint
	if ansi.StringWidth(label) >= width {
		return ansi.Truncate(theme.Text.Tertiary.Render(strings.TrimSpace(label)), width, "…")
	}
	remaining := width - ansi.StringWidth(label)
	left := remaining / 2
	return theme.Text.Tertiary.Render(
		strings.Repeat("─", left) + label + strings.Repeat("─", remaining-left))
}

type recentConversationsMsg struct{ result agent.ConversationListResult }

// fetchRecentConversations reads the offer off the engine's ungated path, so it
// cannot fail the user's first turn with ErrOperationActive. The context and the
// engine call both live inside the returned closure so the read's lifetime (and
// the deadline it starts) begins only when Bubble Tea actually runs the command.
func (m *Model) fetchRecentConversations() tea.Cmd {
	if m.engine == nil {
		return nil
	}
	engine := m.engine
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), historyLoadTimeout)
		defer cancel()
		return recentConversationsMsg{result: <-engine.RecentConversations(ctx)}
	}
}

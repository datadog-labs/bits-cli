package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/conversations"
	"github.com/DataDog/bits-cli/internal/tui/escape"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// resumeRows is how many conversations the offer shows at full height.
const resumeRows = 4

const (
	resumeTitle     = "Resume from a recent conversation with Bits?"
	resumeHint      = " ↑/↓ browse · start typing for a new conversation "
	resumeColumnGap = 2

	// Narrower than this, a title truncates to noise, so the row shows only its
	// timestamp.
	resumeMinTitleWidth = 8
)

// resumeBlockHeight is the block's height for the given row count: a title, a
// blank row, the rows separated by blanks, the scroll indicator, and the hint.
func resumeBlockHeight(rows int) int { return 2*rows + 3 }

// resume is the startup conversation offer. It owns selection and the scroll
// window; visibility is the caller's decision.
type resume struct {
	conversations []assistant.ConversationSummary
	selected      int
	top           int
	now           func() time.Time
}

// setConversations replaces the offer, returning to the first row.
func (r *resume) setConversations(summaries []assistant.ConversationSummary) {
	r.conversations = summaries
	r.reset()
}

func (r *resume) reset() { r.selected, r.top = 0, 0 }

func (r *resume) empty() bool { return len(r.conversations) == 0 }

// move walks the selection by delta, clamping at both ends, and slides the
// window so the selection stays inside it.
func (r *resume) move(delta, visible int) {
	if r.empty() || visible < 1 {
		return
	}
	r.selected = min(max(r.selected+delta, 0), len(r.conversations)-1)
	r.top = min(max(r.top, r.selected-visible+1), r.selected)
	r.top = min(max(r.top, 0), max(0, len(r.conversations)-visible))
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
	lines := []string{
		ansi.Truncate(theme.Text.Primary.Bold(true).Render(resumeTitle), width, "…"),
		"",
	}
	for i := r.top; i < r.top+visible; i++ {
		if i > r.top {
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
		title = ansi.Truncate(escape.SingleLine(conversations.SafeTitle(summary)), titleWidth, "…")
	}
	pad := max(1, width-left-ansi.StringWidth(title)-ansi.StringWidth(stamp))
	row := style.Render(marker+title) + strings.Repeat(" ", pad) + theme.Text.Tertiary.Render(stamp)
	return ansi.Truncate(row, width, "…")
}

// indicator reports what the window hides. The row is always present, blank
// when everything is visible, so scrolling never changes the block's height.
func (r *resume) indicator(theme styles.Theme, width, visible int) string {
	switch below, above := len(r.conversations)-r.top-visible, r.top; {
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

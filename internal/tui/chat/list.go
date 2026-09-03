package chat

import (
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
)

// List is a lazily-rendered, vertically-stacked view of transcript items with an
// owned scroll position. Only items in the visible window are rendered per
// frame.
type List struct {
	width, height int

	// offsetIdx is the first (partially) visible item; offsetLine is how many of
	// its lines (plus the gap after it) are scrolled above the top (>= 0).
	offsetIdx  int
	offsetLine int

	// gap is the number of blank rows between adjacent items.
	gap int

	// follow keeps the view pinned to the tail. Tracked explicitly rather than
	// derived from AtBottom(): the transcript mutates items in place before the
	// list re-syncs, so AtBottom() would already see the grown content.
	follow bool

	items []agent.Block
	sty   Styles

	// frame is the animation step handed to in-flight status chips. It is not
	// part of the cache key: animated blocks bypass the cache entirely (see
	// renderItem), so advancing the frame never invalidates settled blocks.
	frame int

	cache    map[agent.BlockID]listLineEntry
	renderer blockRenderer
}

// listLineEntry memoizes one block's rendered lines (height is len(lines)),
// valid while the block's revision and the list width are unchanged.
type listLineEntry struct {
	rev   int
	width int
	lines []string
}

// NewList returns an empty list with a one-row gap between blocks.
func NewList() *List {
	return &List{gap: 1, follow: true, cache: map[agent.BlockID]listLineEntry{}}
}

// Following reports whether the view is pinned to the tail.
func (l *List) Following() bool { return l.follow }

// SetWidth sets the viewport width, invalidating the cache since wrapping
// depends on width.
func (l *List) SetWidth(w int) {
	if w != l.width {
		l.invalidateAll()
	}
	l.width = w
}

// SetHeight sets the visible height. It does not affect wrapping, so the cache
// stays valid.
func (l *List) SetHeight(h int) { l.height = h }

// Width returns the viewport width.
func (l *List) Width() int { return l.width }

// Height returns the visible height.
func (l *List) Height() int { return l.height }

// SetStyles swaps the render styles and invalidates the cache (old palette).
func (l *List) SetStyles(sty Styles) {
	l.sty = sty
	l.invalidateAll()
}

// SetItems replaces the block slice, preserving the scroll offset but clamping
// it so it never points past the end.
func (l *List) SetItems(items []agent.Block) {
	l.items = items
	if l.offsetIdx >= len(l.items) {
		l.offsetIdx = max(0, len(l.items)-1)
		l.offsetLine = 0
	}
	l.clampOffset()
}

// Reset clears items, scroll state, and rendered-block cache. Conversation
// resets need the cache clear because a new Transcript may reuse synthetic
// block IDs and revisions from the previous conversation.
func (l *List) Reset() {
	l.items = nil
	l.offsetIdx = 0
	l.offsetLine = 0
	l.follow = true
	l.invalidateAll()
}

// clampOffset pulls the offset back so the view never scrolls past the last
// content line.
func (l *List) clampOffset() {
	if len(l.items) == 0 || l.height <= 0 {
		l.offsetIdx, l.offsetLine = 0, 0
		return
	}
	lastIdx, lastLine := l.lastOffsetItem()
	if l.offsetIdx > lastIdx || (l.offsetIdx == lastIdx && l.offsetLine > lastLine) {
		l.offsetIdx, l.offsetLine = lastIdx, lastLine
	}
}

func (l *List) invalidateAll() {
	clear(l.cache)
}

// renderItem returns the block's rendered lines, cached by revision and width.
// Animated blocks are re-rendered every call and never cached, so the
// per-frame cost tracks the number of in-flight tools rather than the length
// of the transcript.
func (l *List) renderItem(idx int) []string {
	it := l.items[idx]
	if animated(it) {
		return strings.Split(l.renderer.RenderBlock(it, l.width, l.sty, l.frame), "\n")
	}
	if e, ok := l.cache[it.ID]; ok && e.rev == it.Rev && e.width == l.width {
		return e.lines
	}
	lines := strings.Split(l.renderer.RenderBlock(it, l.width, l.sty, l.frame), "\n")
	l.cache[it.ID] = listLineEntry{rev: it.Rev, width: l.width, lines: lines}
	return lines
}

// animated reports whether a block's rendering depends on the frame counter.
// Only the two in-flight tool states carry an animated status chip.
//
// Open question for review: ToolAwaitingApproval is included, so the tick keeps
// running for as long as the prompt is unanswered — indefinitely if the user
// walks away. The motion is what draws the eye to something needing action,
// which is why it is here; dropping it from this predicate is the one-line
// change if the idle repaints matter more.
func animated(it agent.Block) bool {
	if it.Tool == nil {
		return false
	}
	return it.Tool.Status == agent.ToolRunning || it.Tool.Status == agent.ToolAwaitingApproval
}

// HasAnimated reports whether any block currently needs the frame counter to
// advance. The tui uses it to arm and disarm the animation tick, so an idle
// transcript costs nothing.
func (l *List) HasAnimated() bool {
	for _, it := range l.items {
		if animated(it) {
			return true
		}
	}
	return false
}

// SetFrame sets the animation step used by in-flight status chips. It does not
// touch the cache, since animated blocks do not use it.
func (l *List) SetFrame(frame int) { l.frame = frame }

func (l *List) itemHeight(idx int) int { return len(l.renderItem(idx)) }

// AtBottom reports whether the last item's bottom is visible. It stops once the
// content exceeds one viewport, so it is O(viewport).
func (l *List) AtBottom() bool {
	if len(l.items) == 0 {
		return true
	}
	total := 0
	for idx := l.offsetIdx; idx < len(l.items); idx++ {
		if total > l.height {
			return false
		}
		h := l.itemHeight(idx)
		if l.gap > 0 && idx > l.offsetIdx {
			h += l.gap
		}
		total += h
	}
	return total-l.offsetLine <= l.height
}

// lastOffsetItem returns the (index, intra-item line offset) that places the
// last content line at the bottom of the viewport.
func (l *List) lastOffsetItem() (int, int) {
	total := 0
	idx := len(l.items) - 1
	for ; idx >= 0; idx-- {
		h := l.itemHeight(idx)
		if l.gap > 0 && idx < len(l.items)-1 {
			h += l.gap
		}
		total += h
		if total > l.height {
			break
		}
	}
	return max(idx, 0), max(total-l.height, 0)
}

// ScrollToTop pins the view to the first item and stops following the tail.
func (l *List) ScrollToTop() {
	l.offsetIdx = 0
	l.offsetLine = 0
	l.follow = false
}

// ScrollToBottom pins the view to the last content line and resumes following.
func (l *List) ScrollToBottom() {
	l.follow = true
	if len(l.items) == 0 {
		l.offsetIdx = 0
		l.offsetLine = 0
		return
	}
	l.offsetIdx, l.offsetLine = l.lastOffsetItem()
}

// ScrollBy scrolls by the given number of lines (positive is down), rendering
// only the items it crosses.
func (l *List) ScrollBy(lines int) {
	if len(l.items) == 0 || lines == 0 {
		return
	}
	// A manual scroll re-derives follow: at the bottom we follow, otherwise not.
	defer func() { l.follow = l.AtBottom() }()

	if lines > 0 {
		if l.AtBottom() {
			return
		}
		l.offsetLine += lines
		for l.offsetLine >= l.itemHeight(l.offsetIdx) {
			l.offsetLine -= l.itemHeight(l.offsetIdx)
			if l.gap > 0 {
				l.offsetLine = max(0, l.offsetLine-l.gap)
			}
			l.offsetIdx++
			if l.offsetIdx > len(l.items)-1 {
				l.ScrollToBottom()
				return
			}
		}
		lastIdx, lastLine := l.lastOffsetItem()
		if l.offsetIdx > lastIdx || (l.offsetIdx == lastIdx && l.offsetLine > lastLine) {
			l.offsetIdx, l.offsetLine = lastIdx, lastLine
		}
		return
	}

	l.offsetLine += lines // negative
	for l.offsetLine < 0 {
		l.offsetIdx--
		if l.offsetIdx < 0 {
			l.ScrollToTop()
			return
		}
		h := l.itemHeight(l.offsetIdx)
		if l.gap > 0 {
			h += l.gap
		}
		l.offsetLine += h
	}
}

// PageUp scrolls up by one viewport height.
func (l *List) PageUp() { l.ScrollBy(-l.height) }

// PageDown scrolls down by one viewport height.
func (l *List) PageDown() { l.ScrollBy(l.height) }

// Render returns exactly Height() lines: the visible slice of the transcript,
// padded with blank lines so content stays top-aligned and the footer stays
// pinned to the bottom.
func (l *List) Render() string {
	// Self-heal the tail pin: streaming growth or a resize can leave the offset
	// above the true bottom, so re-anchor here, the single render boundary.
	if l.follow && !l.AtBottom() {
		l.ScrollToBottom()
	}

	budget := max(l.height, 0)
	lines := make([]string, 0, budget)

	idx := l.offsetIdx
	off := l.offsetLine
	for idx < len(l.items) && len(lines) < budget {
		itemLines := l.renderItem(idx)
		h := len(itemLines)

		if off >= 0 && off < h {
			visible := itemLines[off:]
			if rem := budget - len(lines); len(visible) > rem {
				visible = visible[:rem]
			}
			lines = append(lines, visible...)
			if l.gap > 0 {
				for range min(budget-len(lines), l.gap) {
					lines = append(lines, "")
				}
			}
		} else {
			// The offset starts inside the gap after this item.
			gapRemaining := l.gap - (off - h)
			if gapRemaining > 0 {
				for range min(budget-len(lines), gapRemaining) {
					lines = append(lines, "")
				}
			}
		}

		idx++
		off = 0
	}

	for len(lines) < budget {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

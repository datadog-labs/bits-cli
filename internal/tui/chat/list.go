package chat

import (
	"encoding/binary"
	"hash/fnv"
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
	view  []presentationItem
	sty   Styles

	// frame is the animation step handed to in-flight status indicators. It is not
	// part of the cache key: animated blocks bypass the cache entirely (see
	// renderItem), so advancing the frame never invalidates settled blocks.
	frame int

	cache    map[agent.BlockID]listLineEntry
	renderer blockRenderer
}

// presentationItem points into List.items. It never copies or rewrites source
// blocks; it only records the deterministic presentation grouping.
type presentationItem struct {
	id            agent.BlockID
	rev           uint64
	group         string
	start, end    int
	presentations []toolPresentation
}

// listLineEntry memoizes one block's rendered lines (height is len(lines)),
// valid while the block's revision and the list width are unchanged.
type listLineEntry struct {
	rev   uint64
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
//
// TODO: Revisit the cost of rebuilding presentation data on UI-only updates if
// large transcripts become common.
func (l *List) SetItems(items []agent.Block) {
	l.items = items
	l.view = buildPresentation(items)
	if l.offsetIdx >= len(l.view) {
		l.offsetIdx = max(0, len(l.view)-1)
		l.offsetLine = 0
	}
	l.clampOffset()
}

// Reset clears items, scroll state, and rendered-block cache. Conversation
// resets need the cache clear because a new Transcript may reuse synthetic
// block IDs and revisions from the previous conversation.
func (l *List) Reset() {
	l.items = nil
	l.view = nil
	l.offsetIdx = 0
	l.offsetLine = 0
	l.follow = true
	l.invalidateAll()
}

// clampOffset pulls the offset back so the view never scrolls past the last
// content line.
func (l *List) clampOffset() {
	if len(l.view) == 0 || l.height <= 0 {
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
// Animated items are re-rendered every call and never cached, so the per-frame
// cost tracks the number of in-flight tools rather than transcript length.
func (l *List) renderItem(idx int) []string {
	it := l.view[idx]
	if l.itemAnimated(it) {
		return strings.Split(l.renderPresentationItem(it), "\n")
	}
	if e, ok := l.cache[it.id]; ok && e.rev == it.rev && e.width == l.width {
		return e.lines
	}
	lines := strings.Split(l.renderPresentationItem(it), "\n")
	l.cache[it.id] = listLineEntry{rev: it.rev, width: l.width, lines: lines}
	return lines
}

func (l *List) renderPresentationItem(it presentationItem) string {
	if it.group == inspectionGroupKey {
		return renderInspectionGroup(l.items[it.start:it.end], it.presentations, l.width, l.sty, l.frame)
	}
	if len(it.presentations) == 1 {
		return renderPresentedTool(l.items[it.start].Tool, it.presentations[0], l.width, l.sty, l.frame)
	}
	return l.renderer.RenderBlock(l.items[it.start], l.width, l.sty, l.frame)
}

// itemAnimated reports whether rendering depends on the frame counter. Waiting
// for approval is deliberately static because no work is progressing.
func (l *List) itemAnimated(it presentationItem) bool {
	for i := it.start; i < it.end; i++ {
		if lifecycleOf(l.items[i].Tool) == lifecycleRunning {
			return true
		}
	}
	return false
}

// HasAnimated reports whether any block currently needs the frame counter to
// advance. The tui uses it to arm and disarm the animation tick, so an idle
// transcript costs nothing.
func (l *List) HasAnimated() bool {
	for _, it := range l.view {
		if l.itemAnimated(it) {
			return true
		}
	}
	return false
}

// SetFrame sets the animation step used by in-flight indicators. It does not
// touch the cache, since animated items do not use it.
func (l *List) SetFrame(frame int) { l.frame = frame }

func (l *List) itemHeight(idx int) int { return len(l.renderItem(idx)) }

// AtBottom reports whether the last item's bottom is visible. It stops once the
// content exceeds one viewport, so it is O(viewport).
func (l *List) AtBottom() bool {
	if len(l.view) == 0 {
		return true
	}
	total := 0
	for idx := l.offsetIdx; idx < len(l.view); idx++ {
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
	idx := len(l.view) - 1
	for ; idx >= 0; idx-- {
		h := l.itemHeight(idx)
		if l.gap > 0 && idx < len(l.view)-1 {
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
	if len(l.view) == 0 {
		l.offsetIdx = 0
		l.offsetLine = 0
		return
	}
	l.offsetIdx, l.offsetLine = l.lastOffsetItem()
}

// ScrollBy scrolls by the given number of lines (positive is down), rendering
// only the items it crosses.
func (l *List) ScrollBy(lines int) {
	if len(l.view) == 0 || lines == 0 {
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
			if l.offsetIdx > len(l.view)-1 {
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
	for idx < len(l.view) && len(lines) < budget {
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

func buildPresentation(blocks []agent.Block) []presentationItem {
	items := make([]presentationItem, 0, len(blocks))
	for i := 0; i < len(blocks); {
		p, isTool := presentationOfBlock(blocks[i])
		if !isTool || p.group == "" {
			var presentation []toolPresentation
			if isTool {
				presentation = []toolPresentation{p}
			}
			items = append(items, newPresentationItem(blocks, "", i, i+1, presentation))
			i++
			continue
		}

		presentations := []toolPresentation{p}
		j := i + 1
		for j < len(blocks) {
			next, ok := presentationOfBlock(blocks[j])
			if !ok || next.group != p.group {
				break
			}
			presentations = append(presentations, next)
			j++
		}
		items = append(items, newPresentationItem(blocks, p.group, i, j, presentations))
		i = j
	}
	return items
}

func presentationOfBlock(block agent.Block) (toolPresentation, bool) {
	if block.Tool == nil {
		return toolPresentation{}, false
	}
	return classifyTool(block.Tool), true
}

func newPresentationItem(blocks []agent.Block, group string, start, end int, presentations []toolPresentation) presentationItem {
	first := blocks[start].ID
	it := presentationItem{
		id:            first,
		group:         group,
		start:         start,
		end:           end,
		presentations: presentations,
		rev:           presentationRevision(blocks, start, end),
	}
	return it
}

// presentationRevision is FNV-1a over every member identity and revision. The
// stable first BlockID keys its cache entry, so appending to or updating a
// group invalidates its cached rendering without moving the item.
func presentationRevision(blocks []agent.Block, start, end int) uint64 {
	h := fnv.New64a()
	var bytes [8]byte
	mixUint := func(v uint64) {
		binary.LittleEndian.PutUint64(bytes[:], v)
		_, _ = h.Write(bytes[:])
	}
	mixString := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0xff})
	}
	for i := start; i < end; i++ {
		block := blocks[i]
		mixUint(uint64(block.ID.Scope))
		mixString(block.ID.Key)
		mixUint(uint64(block.ID.Kind))
		mixUint(uint64(block.Rev))
	}
	return h.Sum64()
}

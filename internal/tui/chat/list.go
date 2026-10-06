package chat

import (
	"encoding/binary"
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/components"
	"github.com/DataDog/bits-cli/internal/tui/escape"
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

	items   []agent.Block
	notices []NoticeItem
	view    []presentationItem
	sty     Styles

	// header is pre-rendered content occupying the document's first rows. It
	// becomes a synthetic presentation item so the scroll, document-coordinate
	// and selection paths need no knowledge of it.
	header string

	// frame is the animation step handed to in-flight status indicators. Animated
	// cache entries include it in their identity, while settled entries ignore it.
	frame int

	cache    map[agent.BlockID]listLineEntry
	renderer blockRenderer

	// gutterWidth is the accordion control's width, fixed per style set; 0
	// (before the first SetStyles) disables the gutter.
	gutterWidth int

	// expandAll is the ctrl+o default for every guttered block; toggled holds
	// per-block clicks that invert it. ctrl+o clears toggled, so the default
	// applies uniformly, including to blocks that arrive later. Blocks start
	// collapsed: a tool shows its compact view until expanded.
	expandAll bool
	toggled   map[agent.BlockID]bool

	// pointerY is the viewport row under the mouse, or -1 when unknown. Hover
	// is derived from it at render time, so it can never go stale when the
	// content moves under a stationary pointer.
	pointerY int
}

// Surface is the visible transcript and its row offset in the full document.
type Surface struct {
	Content string
	Top     int
}

// presentationItem points into List.items. It never copies or rewrites source
// blocks; it only records the deterministic presentation grouping.
type presentationItem struct {
	id            agent.BlockID
	rev           uint64
	kind          itemKind
	start, end    int
	presentations []toolPresentation
	notice        *NoticeItem
}

// itemKind is the shape of a presentation item, decided once when the
// presentation is built.
type itemKind uint8

const (
	itemBlock itemKind = iota
	itemNotice
	// itemTool is one tool call, including an inspection call with no
	// adjacent peers. Its presentations hold exactly that call.
	itemTool
	itemReasoning
	// itemInspectionGroup is a run of two or more adjacent inspection calls.
	itemInspectionGroup
	// itemHeader is the synthetic header. Its start/end are headerSentinel:
	// it has no backing block, and every reader of start must tolerate that.
	itemHeader
)

const headerSentinel = -1

// headerBlockID keys the header's cache entry. The NUL-prefixed key cannot
// collide with a client-minted or wire id, which keeps this a view-layer
// concern rather than a new BlockScope in the agent package.
var headerBlockID = agent.BlockID{Scope: agent.ScopeLocal, Key: "\x00header"}

// listLineEntry memoizes one block's rendered lines (height is len(lines)),
// including the accordion gutter and disclosure mode. An animated entry is valid only
// for the frame that produced it; a settled entry remains valid as the global
// animation frame advances.
type listLineEntry struct {
	rev        uint64
	width      int
	animated   bool
	frame      int
	disclosure disclosure
	lines      []string
}

// NewList returns an empty list with a one-row gap between blocks.
func NewList() *List {
	return &List{gap: 1, follow: true, cache: map[agent.BlockID]listLineEntry{}, toggled: map[agent.BlockID]bool{}, pointerY: -1}
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
	l.normalizeOffset()
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
	l.gutterWidth = components.AccordionWidth(sty.Accordion)
	l.invalidateAll()
}

// SetItems replaces the block slice, preserving the scroll offset but clamping
// it so it never points past the end.
//
// TODO: Revisit the cost of rebuilding presentation data on UI-only updates if
// large transcripts become common.
func (l *List) SetItems(items []agent.Block) {
	l.SetTranscript(items, nil)
}

// SetTranscript combines agent blocks with session-only UI messages without
// changing the source blocks or their backend-facing transcript.
func (l *List) SetTranscript(items []agent.Block, notices []NoticeItem) {
	l.items = items
	l.notices = notices
	l.view = buildPresentationWithNotices(l.header, items, notices)
	if l.offsetIdx >= len(l.view) {
		l.offsetIdx = max(0, len(l.view)-1)
		l.offsetLine = 0
	}
	l.clampOffset()
}

// SetHeader replaces the document's leading content. Passing "" removes it.
//
// Adding or removing the header renumbers every presentation item, and
// normalizeOffset carries an offset only through a changed height, not a
// shifted index, so a scrolled offset is rebased here.
func (l *List) SetHeader(header string) {
	if header == l.header {
		return
	}
	had, has := l.header != "", header != ""
	l.header = header
	l.view = buildPresentationWithNotices(l.header, l.items, l.notices)
	switch {
	case had == has:
	case has:
		// Every item moved down one, so an offset into content follows it. One
		// resting at the very top stays, or the new header is scrolled past.
		if l.offsetIdx > 0 || l.offsetLine > 0 {
			l.offsetIdx++
		}
	case l.offsetIdx > 0:
		l.offsetIdx--
	default:
		// The offset pointed inside the header, which is gone, so there is no
		// row to carry.
		l.offsetLine = 0
	}
	l.normalizeOffset()
}

func headerRevision(header string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(header))
	return h.Sum64()
}

// Reset clears items, scroll state, and rendered-block cache. Conversation
// resets need the cache clear because a new Transcript may reuse synthetic
// block IDs and revisions from the previous conversation.
func (l *List) Reset() {
	l.items = nil
	l.notices = nil
	l.view = buildPresentation(l.header, nil)
	l.offsetIdx = 0
	l.offsetLine = 0
	l.follow = true
	l.expandAll = false
	clear(l.toggled)
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

// normalizeOffset carries an offset past items whose rendered height changed.
// This can happen after a resize changes wrapping while the list is scrolled.
// Keep offsets inside an item or its following gap so rendering and document
// coordinates continue to describe the same row.
func (l *List) normalizeOffset() {
	if len(l.view) == 0 {
		l.offsetIdx, l.offsetLine = 0, 0
		return
	}
	l.offsetLine = max(l.offsetLine, 0)
	for l.offsetIdx < len(l.view) {
		h := l.itemHeight(l.offsetIdx)
		span := h + l.gapAfter(l.offsetIdx)
		if l.offsetLine < span {
			return
		}
		l.offsetLine -= span
		l.offsetIdx++
	}
	l.clampOffset()
}

func (l *List) invalidateAll() {
	clear(l.cache)
}

// gapAfter keeps adjacent agent-activity rows together while preserving the
// normal conversation spacing at either edge. It operates on presentation
// items so an inspection group behaves as one stacked row regardless of how
// many source tool blocks it contains.
func (l *List) gapAfter(idx int) int {
	if idx < 0 || idx >= len(l.view) {
		return max(l.gap, 0)
	}

	left := l.view[idx]
	base := max(l.gap, 0)
	if idx+1 < len(l.view) {
		right := l.view[idx+1]
		if l.stacksTight(left) && l.stacksTight(right) {
			base = 0
		}
		return max(base, left.spacing().after, right.spacing().before)
	}
	return max(base, left.spacing().after)
}

func (it presentationItem) spacing() itemSpacing {
	if it.kind != itemTool {
		return itemSpacing{}
	}
	return it.presentations[0].renderSpec.spacing
}

func (l *List) stacksTight(it presentationItem) bool {
	if it.start < 0 || it.start >= len(l.items) {
		return false
	}
	switch l.items[it.start].Kind {
	case assistant.KindReasoning, assistant.KindToolCall, assistant.KindToolResult:
		return true
	default:
		return false
	}
}

// gutter returns the accordion gutter width it reserves: the control's width
// for an expandable item when the viewport has room left for content, 0
// otherwise.
func (l *List) gutter(it presentationItem) int {
	if l.gutterWidth == 0 || l.width <= l.gutterWidth || !l.expandable(it) {
		return 0
	}
	return l.gutterWidth
}

// expandable reports whether an item has a full view to disclose. Every single
// tool does, unless its renderer is static; a reasoning group does when it has
// text. Multi-tool inspection groups keep their left edge.
func (l *List) expandable(it presentationItem) bool {
	switch it.kind {
	case itemTool:
		return !it.presentations[0].renderSpec.static
	case itemReasoning:
		return hasReasoningText(l.items[it.start:it.end])
	case itemBlock, itemNotice, itemInspectionGroup, itemHeader:
	}
	return false
}

func (l *List) expanded(id agent.BlockID) bool { return l.expandAll != l.toggled[id] }

// ToggleDisclosure flips one block between its compact and full view.
func (l *List) ToggleDisclosure(id agent.BlockID) {
	l.toggled[id] = !l.toggled[id]
	l.anchorOffset()
}

// ToggleAllDisclosure expands every guttered block, or collapses them all if
// they were expanded, discarding individual toggles.
func (l *List) ToggleAllDisclosure() {
	l.expandAll = !l.expandAll
	clear(l.toggled)
	l.anchorOffset()
}

// anchorOffset keeps the viewport on the current item when its height shrinks.
func (l *List) anchorOffset() {
	if l.offsetIdx < len(l.view) {
		l.offsetLine = min(l.offsetLine, max(l.itemHeight(l.offsetIdx)-1, 0))
	}
}

// SetPointerRow records the viewport row under the mouse; any row outside the
// viewport clears hover.
func (l *List) SetPointerRow(y int) { l.pointerY = y }

// Hovered reports whether the pointer sits over a clickable accordion row.
func (l *List) Hovered() bool {
	_, ok := l.HeaderAt(l.pointerY)
	return ok
}

// HeaderAt returns the block whose clickable accordion header sits on viewport
// row y. The whole row is the target, so only y matters.
func (l *List) HeaderAt(y int) (agent.BlockID, bool) {
	if y < 0 || y >= l.height {
		return agent.BlockID{}, false
	}
	l.settle()
	row := -l.offsetLine
	for idx := l.offsetIdx; idx < len(l.view) && row <= y; idx++ {
		e := l.entry(idx)
		if row == y && l.gutter(l.view[idx]) > 0 {
			return l.view[idx].id, true
		}
		row += len(e.lines) + l.gapAfter(idx)
	}
	return agent.BlockID{}, false
}

func (l *List) renderItem(idx int) []string { return l.entry(idx).lines }

// entry returns the block's rendered lines, cached by revision, width and
// disclosure state. Animated entries additionally key on frame, so unrelated
// model updates at the same frame do not render them again; settled entries
// remain cached as the global animation frame advances.
func (l *List) entry(idx int) listLineEntry {
	it := l.view[idx]
	gutter := l.gutter(it)
	width := l.width - gutter
	animated := l.itemAnimated(it)
	d := compactView
	if gutter > 0 && l.expanded(it.id) {
		d = fullView
	}
	if e, ok := l.cache[it.id]; ok &&
		e.rev == it.rev && e.width == width && e.animated == animated && e.disclosure == d &&
		(!animated || e.frame == l.frame) {
		return e
	}
	content := l.renderPresentationItem(it, renderContext{width: width, sty: l.sty, frame: l.frame, disclosure: d})
	e := listLineEntry{
		rev:        it.rev,
		width:      width,
		animated:   animated,
		frame:      l.frame,
		disclosure: d,
		lines:      strings.Split(content, "\n"),
	}
	if gutter > 0 {
		e.lines = l.addGutter(e.lines, d == fullView)
	}
	l.cache[it.id] = e
	return e
}

// addGutter splices the accordion control into a tool's header and indents
// its detail rows. The renderer already chose the compact or full view, so the
// gutter only reflects that state. The status glyph stays the leftmost cell,
// so the control goes right after it.
func (l *List) addGutter(lines []string, expanded bool) []string {
	blank := strings.Repeat(" ", l.gutterWidth)
	control := components.Accordion(l.sty.Accordion, expanded)
	out := make([]string, len(lines))
	out[0] = ansi.Cut(lines[0], 0, statusGlyphWidth) + control + ansi.TruncateLeft(lines[0], statusGlyphWidth, "")
	for i := 1; i < len(lines); i++ {
		out[i] = blank + lines[i]
	}
	return out
}

func (l *List) renderPresentationItem(it presentationItem, c renderContext) string {
	// First: the header has no backing block, so it must return before any
	// branch indexes l.items.
	switch it.kind {
	case itemHeader:
		return l.header
	case itemNotice:
		return renderNotice(it.notice.Notice, c.width, c.sty)
	case itemReasoning:
		return renderReasoningGroup(l.items[it.start:it.end], c)
	case itemInspectionGroup:
		return renderInspectionGroup(l.items[it.start:it.end], it.presentations, c)
	case itemTool:
		return renderPresentedTool(l.items[it.start].Tool, it.presentations[0], c)
	case itemBlock:
	}
	return l.renderer.RenderBlock(l.items[it.start], c.width, c.sty, c.frame)
}

// itemAnimated reports whether rendering depends on the frame counter. Waiting
// for approval is deliberately static because no work is progressing.
func (l *List) itemAnimated(it presentationItem) bool {
	if it.kind == itemNotice || it.kind == itemHeader {
		return false
	}
	// WithoutMotion replaces the shared spinner with static fallbacks, so
	// running blocks no longer justify a repaint clock.
	if l.sty.StatusSpinner.Len() == 0 {
		return false
	}
	for i := it.start; i < it.end; i++ {
		block := l.items[i]
		if statusOf(block.Tool) == agent.ToolRunning || (block.Kind == assistant.KindReasoning && !block.Complete) {
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

// SetFrame sets the animation step used by in-flight indicators. Cache entries
// invalidate lazily: animated entries compare frames, settled entries do not.
func (l *List) SetFrame(frame int) { l.frame = frame }

func (l *List) itemHeight(idx int) int { return len(l.renderItem(idx)) }

// AtBottom reports whether the last item's bottom is visible. It stops once the
// content exceeds one viewport, so it is O(viewport).
func (l *List) AtBottom() bool {
	if len(l.view) == 0 {
		return true
	}
	// Start at the viewport's first row, which may be deep inside a tall
	// item (notably the splash). Checking an item's full height before
	// subtracting offsetLine would report false even with the tail visible.
	total := -l.offsetLine
	for idx := l.offsetIdx; idx < len(l.view); idx++ {
		h := l.itemHeight(idx)
		if idx > l.offsetIdx {
			h += l.gapAfter(idx - 1)
		}
		total += h
		if total > l.height {
			return false
		}
	}
	return true
}

// lastOffsetItem returns the (index, intra-item line offset) that places the
// last content line at the bottom of the viewport.
func (l *List) lastOffsetItem() (int, int) {
	total := 0
	idx := len(l.view) - 1
	for ; idx >= 0; idx-- {
		h := l.itemHeight(idx)
		if idx < len(l.view)-1 {
			h += l.gapAfter(idx)
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
	l.settle()
	// A manual scroll re-derives follow: at the bottom we follow, otherwise not.
	defer func() { l.follow = l.AtBottom() }()

	if lines > 0 {
		if l.AtBottom() {
			return
		}
		l.offsetLine += lines
		for l.offsetIdx < len(l.view)-1 && l.offsetLine >= l.itemHeight(l.offsetIdx)+l.gapAfter(l.offsetIdx) {
			l.offsetLine -= l.itemHeight(l.offsetIdx) + l.gapAfter(l.offsetIdx)
			l.offsetIdx++
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
		h := l.itemHeight(l.offsetIdx) + l.gapAfter(l.offsetIdx)
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
	return l.renderSurface(false).Content
}

// VisibleSurface returns exactly Height() visible transcript lines, padded
// with blank lines so content stays top-aligned and the footer stays pinned to
// the bottom. Top is the full rendered transcript row represented by the
// viewport's first row.
func (l *List) VisibleSurface() Surface {
	return l.renderSurface(true)
}

// settle self-heals the tail pin: streaming growth or a resize can leave the
// offset above the true bottom, and a taller viewport can leave it past the
// end. Rendering, hit-testing, and scrolling all call it, so each sees the rows
// render will draw.
func (l *List) settle() {
	l.normalizeOffset()
	switch {
	case l.follow:
		l.ScrollToBottom()
	case l.height > 0:
		l.clampOffset()
	}
}

func (l *List) renderSurface(withPosition bool) Surface {
	l.settle()

	budget := max(l.height, 0)
	lines := make([]string, 0, budget)
	idx := l.offsetIdx
	off := l.offsetLine
	for idx < len(l.view) && len(lines) < budget {
		itemLines := l.renderItem(idx)
		h := len(itemLines)
		gap := l.gapAfter(idx)

		if off >= 0 && off < h {
			visible := itemLines[off:]
			if rem := budget - len(lines); len(visible) > rem {
				visible = visible[:rem]
			}
			lines = append(lines, visible...)
			if gap > 0 {
				for range min(budget-len(lines), gap) {
					lines = append(lines, "")
				}
			}
		} else {
			// The offset starts inside the gap after this item.
			gapRemaining := gap - (off - h)
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
	if l.Hovered() {
		lines[l.pointerY] = components.PaintRowBackground(lines[l.pointerY], l.width, l.sty.Accordion.HoverBackground)
	}
	top := 0
	if withPosition {
		top = l.offsetRow()
	}
	return Surface{Content: strings.Join(lines, "\n"), Top: top}
}

// HeaderRows is how many document rows the header owns, including the gap
// after it, and 0 when there is no header. Callers that read meaning from
// document rows — text selection — need it to tell decoration from transcript.
// It follows Document's item-plus-gap accounting.
func (l *List) HeaderRows() int {
	if l.header == "" || len(l.view) == 0 {
		return 0
	}
	return len(l.renderItem(0)) + max(l.gapAfter(0), 0)
}

// Document renders the complete transcript without viewport fill rows or
// changing the current scroll position.
func (l *List) Document() string {
	lines := make([]string, 0)
	for idx := range l.view {
		lines = append(lines, l.renderItem(idx)...)
		for range max(l.gapAfter(idx), 0) {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

// offsetRow converts the list's item/intra-item scroll state into the row
// coordinate used by Document. It intentionally follows the same
// item-plus-gap accounting as Render and AtBottom.
func (l *List) offsetRow() int {
	row := 0
	for idx := 0; idx < l.offsetIdx && idx < len(l.view); idx++ {
		row += l.itemHeight(idx) + l.gapAfter(idx)
	}
	return row + max(l.offsetLine, 0)
}

// ScrollByChanged scrolls by lines and reports whether the viewport position
// (or its follow state) changed. The existing ScrollBy remains the mutating
// primitive for callers that do not need the result.
func (l *List) ScrollByChanged(lines int) bool {
	l.settle()
	idx, line, follow := l.offsetIdx, l.offsetLine, l.follow
	l.ScrollBy(lines)
	return idx != l.offsetIdx || line != l.offsetLine || follow != l.follow
}

func buildPresentation(header string, blocks []agent.Block) []presentationItem {
	return buildPresentationWithNotices(header, blocks, nil)
}

func buildPresentationWithNotices(header string, blocks []agent.Block, notices []NoticeItem) []presentationItem {
	items := make([]presentationItem, 0, len(blocks)+1)
	if header != "" {
		items = append(items, presentationItem{
			id:    headerBlockID,
			rev:   headerRevision(header),
			kind:  itemHeader,
			start: headerSentinel,
			end:   headerSentinel,
		})
	}
	// A local message divides presentation groups, so streamed reasoning or
	// adjacent tools cannot later absorb a message posted between them.
	previous := 0
	for i := range notices {
		at := min(max(notices[i].After, previous), len(blocks))
		items = appendBlockPresentations(items, blocks, previous, at)
		entry := &notices[i]
		items = append(items, presentationItem{
			id:   agent.BlockID{Scope: agent.ScopeLocal, Key: "\x00notice:" + strconv.FormatUint(entry.ID, 10)},
			rev:  uint64(entry.Notice.Level) + headerRevision(entry.Notice.Text),
			kind: itemNotice, start: headerSentinel, end: headerSentinel, notice: entry,
		})
		previous = at
	}
	return appendBlockPresentations(items, blocks, previous, len(blocks))
}

func appendBlockPresentations(items []presentationItem, blocks []agent.Block, from, to int) []presentationItem {
	for i := from; i < to; {
		if blocks[i].Kind == assistant.KindReasoning {
			j := i + 1
			for j < to && blocks[j].Kind == assistant.KindReasoning {
				j++
			}
			items = append(items, newPresentationItem(blocks, itemReasoning, i, j, nil))
			i = j
			continue
		}

		p, isTool := presentationOfBlock(blocks[i])
		if !isTool {
			items = append(items, newPresentationItem(blocks, itemBlock, i, i+1, nil))
			i++
			continue
		}

		presentations := []toolPresentation{p}
		group := p.group()
		j := i + 1
		for group != "" && j < to {
			next, ok := presentationOfBlock(blocks[j])
			if !ok || next.group() != group {
				break
			}
			presentations = append(presentations, next)
			j++
		}
		kind := itemTool
		if len(presentations) > 1 {
			// inspectionGroupKey is the only tool group key.
			kind = itemInspectionGroup
		}
		items = append(items, newPresentationItem(blocks, kind, i, j, presentations))
		i = j
	}
	return items
}

func renderNotice(n Notice, width int, sty Styles) string {
	marker := ""
	switch n.Level {
	case NoticeInfo:
		marker = "✓"
	case NoticeWarn:
		marker = "!"
	case NoticeError:
		marker = "×"
	}
	// Escape untrusted text before wrapping it into selectable document rows.
	text := escape.Multiline(n.Text)
	lines := strings.Split(wrap(text, max(1, width-3)), "\n")
	for i, line := range lines {
		prefix := "   "
		if i == 0 {
			prefix = sty.Notice(n.Level).Render(marker) + "  "
		}
		lines[i] = components.PaintRowBackground(prefix+line, width, sty.Input.Background)
	}
	return strings.Join(lines, "\n")
}

func presentationOfBlock(block agent.Block) (toolPresentation, bool) {
	if block.Tool == nil {
		return toolPresentation{}, false
	}
	return classifyTool(block.Tool), true
}

func newPresentationItem(blocks []agent.Block, kind itemKind, start, end int, presentations []toolPresentation) presentationItem {
	first := blocks[start].ID
	it := presentationItem{
		id:            first,
		kind:          kind,
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

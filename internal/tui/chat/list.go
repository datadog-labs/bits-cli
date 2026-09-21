package chat

import (
	"encoding/binary"
	"hash/fnv"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/components"
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

	// header is pre-rendered content occupying the document's first rows. It
	// becomes a synthetic presentation item so the scroll, document-coordinate
	// and selection paths need no knowledge of it.
	header string

	// frame is the animation step handed to in-flight status indicators. Animated
	// cache entries include it in their identity, while settled entries ignore it.
	frame int

	cache    map[agent.BlockID]listLineEntry
	renderer blockRenderer

	// accordion renders the disclosure control guttered items reserve space
	// for. It is nil until the first SetStyles call; every real render path
	// (newShell → applyStyles) calls SetStyles before any render happens.
	accordion *components.Accordion

	// collapsed holds only blocks a user (or ctrl+o) has explicitly collapsed;
	// absent means expanded. hovered/hasHover track pointer hover as an
	// explicit pair because agent.BlockID is a comparable struct with no zero
	// value that safely means "nothing".
	collapsed map[agent.BlockID]bool
	hovered   agent.BlockID
	hasHover  bool
	// expandAll is the state the next ToggleAllDisclosure call moves every
	// guttered block to; it flips after each call. Starting true means the
	// first ctrl+o expands, which is a no-op from the default all-expanded
	// state — intentional, not a smoothing bug.
	expandAll bool

	// zones records each guttered item's clickable header row for the most
	// recent renderSurface call, in that surface's own screen coordinates.
	zones []accordionZone
}

// accordionZone is one guttered item's clickable header row, recorded during
// renderSurface. row is a screen row of the viewport that produced it: 0 is
// the surface's own top row, matching the coordinates a caller already has
// from a mouse event over the transcript.
type accordionZone struct {
	id    agent.BlockID
	row   int
	width int
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
	group         string
	start, end    int
	presentations []toolPresentation
}

// headerGroupKey marks the synthetic header item. Its start/end are
// headerSentinel: it has no backing block, and every reader of start must
// tolerate that.
const (
	headerGroupKey = "header"
	headerSentinel = -1
)

// headerBlockID keys the header's cache entry. The NUL-prefixed key cannot
// collide with a client-minted or wire id, which keeps this a view-layer
// concern rather than a new BlockScope in the agent package.
var headerBlockID = agent.BlockID{Scope: agent.ScopeLocal, Key: "\x00header"}

// listLineEntry memoizes one block's rendered lines (height is len(lines)). An
// animated entry is valid only for the frame that produced it; a settled entry
// remains valid as the global animation frame advances.
type listLineEntry struct {
	rev      uint64
	width    int
	animated bool
	frame    int
	lines    []string
}

// NewList returns an empty list with a one-row gap between blocks.
func NewList() *List {
	return &List{gap: 1, follow: true, cache: map[agent.BlockID]listLineEntry{}, collapsed: map[agent.BlockID]bool{}, expandAll: true}
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
	if l.accordion == nil {
		l.accordion = components.NewAccordion(sty.Accordion)
	} else {
		l.accordion.SetStyles(sty.Accordion)
	}
	l.invalidateAll()
}

// SetItems replaces the block slice, preserving the scroll offset but clamping
// it so it never points past the end.
//
// TODO: Revisit the cost of rebuilding presentation data on UI-only updates if
// large transcripts become common.
func (l *List) SetItems(items []agent.Block) {
	l.items = items
	l.view = buildPresentation(l.header, items)
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
	l.view = buildPresentation(l.header, l.items)
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
	l.view = buildPresentation(l.header, nil)
	l.offsetIdx = 0
	l.offsetLine = 0
	l.follow = true
	l.collapsed = map[agent.BlockID]bool{}
	l.hovered = agent.BlockID{}
	l.hasHover = false
	l.expandAll = true
	l.zones = nil
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
	if it.group != "" || len(it.presentations) != 1 || it.presentations[0].renderSpec == nil {
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

// guttered reports whether idx's presentation item reserves the accordion
// gutter. Only a single tool renders one clickable control; reasoning groups
// and multi-tool inspection groups keep their current left edge.
func (l *List) guttered(it presentationItem) bool {
	return len(it.presentations) == 1
}

// hasGutterRoom reports whether the viewport is wide enough to reserve the
// accordion's fixed-width gutter and still leave room for content.
func (l *List) hasGutterRoom() bool {
	return l.accordion != nil && l.width > l.accordion.Width()
}

// itemWidth is the width renderPresentationItem wraps to: narrowed by the
// gutter for a guttered item with room for one, the full width otherwise.
// Total rendered output (gutter + content) never exceeds l.width.
func (l *List) itemWidth(it presentationItem) int {
	if l.guttered(it) && l.hasGutterRoom() {
		return l.width - l.accordion.Width()
	}
	return l.width
}

// ToggleDisclosure flips one block's collapsed state. A block absent from
// collapsed is expanded, so toggling an unseen id collapses it.
func (l *List) ToggleDisclosure(id agent.BlockID) {
	l.collapsed[id] = !l.collapsed[id]
}

// ToggleAllDisclosure sets every guttered block to expandAll's value, then
// flips expandAll so the next call reverses it. This universally overrides
// any individual ToggleDisclosure calls.
func (l *List) ToggleAllDisclosure() {
	for _, it := range l.view {
		if l.guttered(it) {
			l.collapsed[it.id] = !l.expandAll
		}
	}
	l.expandAll = !l.expandAll
}

// SetHovered updates which block's control is hovered and reports whether the
// hover target actually changed, so a caller repaints only when it must.
func (l *List) SetHovered(id agent.BlockID, ok bool) bool {
	if ok == l.hasHover && (!ok || id == l.hovered) {
		return false
	}
	l.hasHover, l.hovered = ok, id
	return true
}

// ZoneAt reports the block whose accordion control occupies screen
// coordinate (x, y), or false if none does. Coordinates are relative to the
// top-left of the surface renderSurface last produced.
func (l *List) ZoneAt(x, y int) (agent.BlockID, bool) {
	for _, z := range l.zones {
		if z.row == y && x >= 0 && x < z.width {
			return z.id, true
		}
	}
	return agent.BlockID{}, false
}

// renderItem returns the block's rendered lines, cached by revision and width.
// Animated entries additionally key on frame, so unrelated model updates at the
// same frame do not render them again; settled entries remain cached as the
// global animation frame advances.
func (l *List) renderItem(idx int) []string {
	it := l.view[idx]
	width := l.itemWidth(it)
	animated := l.itemAnimated(it)
	if e, ok := l.cache[it.id]; ok &&
		e.rev == it.rev && e.width == width && e.animated == animated &&
		(!animated || e.frame == l.frame) {
		return e.lines
	}
	lines := strings.Split(l.renderPresentationItem(it, width), "\n")
	l.cache[it.id] = listLineEntry{
		rev:      it.rev,
		width:    width,
		animated: animated,
		frame:    l.frame,
		lines:    lines,
	}
	return lines
}

// itemLines returns idx's rendered lines with collapse and the accordion
// gutter applied. renderSurface, Document, and itemHeight all read through
// this single helper so the visible surface and the copyable document never
// disagree about which column a cell sits in.
func (l *List) itemLines(idx int) []string {
	it := l.view[idx]
	lines := l.renderItem(idx)
	if !l.guttered(it) || !l.hasGutterRoom() {
		return lines
	}

	hasDisclosure := len(lines) > 1
	expanded := !l.collapsed[it.id]
	if hasDisclosure && !expanded {
		lines = lines[:1]
	}

	gutterWidth := l.accordion.Width()
	control := strings.Repeat(" ", gutterWidth)
	if hasDisclosure {
		control = l.accordion.Render(components.AccordionState{
			Expanded: expanded,
			Hovered:  l.hasHover && l.hovered == it.id,
		})
	}
	pad := strings.Repeat(" ", gutterWidth)

	out := make([]string, len(lines))
	out[0] = control + lines[0]
	for i := 1; i < len(lines); i++ {
		out[i] = pad + lines[i]
	}
	return out
}

// chevronDrawn reports whether idx's item actually drew a clickable chevron,
// as opposed to reserving a blank gutter with nothing to disclose.
func (l *List) chevronDrawn(idx int) bool {
	it := l.view[idx]
	return l.guttered(it) && l.hasGutterRoom() && len(l.renderItem(idx)) > 1
}

func (l *List) renderPresentationItem(it presentationItem, width int) string {
	// First: the header has no backing block, so it must return before any
	// branch indexes l.items.
	if it.group == headerGroupKey {
		return l.header
	}
	if it.group == reasoningGroupKey {
		return renderReasoningGroup(l.items[it.start:it.end], width, l.sty, l.frame)
	}
	if it.group == inspectionGroupKey && len(it.presentations) > 1 {
		return renderInspectionGroup(l.items[it.start:it.end], it.presentations, width, l.sty, l.frame)
	}
	if len(it.presentations) == 1 {
		return renderPresentedTool(l.items[it.start].Tool, it.presentations[0], width, l.sty, l.frame)
	}
	return l.renderer.RenderBlock(l.items[it.start], width, l.sty, l.frame)
}

// itemAnimated reports whether rendering depends on the frame counter. Waiting
// for approval is deliberately static because no work is progressing.
func (l *List) itemAnimated(it presentationItem) bool {
	// WithoutMotion replaces the shared spinner with static fallbacks, so
	// running blocks no longer justify a repaint clock.
	if l.sty.StatusSpinner.Len() == 0 {
		return false
	}
	for i := it.start; i < it.end; i++ {
		block := l.items[i]
		if lifecycleOf(block.Tool) == lifecycleRunning || (block.Kind == assistant.KindReasoning && !block.Complete) {
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

func (l *List) itemHeight(idx int) int { return len(l.itemLines(idx)) }

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
		if idx > l.offsetIdx {
			h += l.gapAfter(idx - 1)
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
	// A manual scroll re-derives follow: at the bottom we follow, otherwise not.
	defer func() { l.follow = l.AtBottom() }()

	if lines > 0 {
		if l.AtBottom() {
			return
		}
		l.offsetLine += lines
		for l.offsetLine >= l.itemHeight(l.offsetIdx) {
			l.offsetLine -= l.itemHeight(l.offsetIdx)
			l.offsetLine = max(0, l.offsetLine-l.gapAfter(l.offsetIdx))
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

func (l *List) renderSurface(withPosition bool) Surface {
	// Self-heal the tail pin: streaming growth or a resize can leave the offset
	// above the true bottom, so re-anchor here, the single render boundary.
	l.normalizeOffset()
	if l.follow && !l.AtBottom() {
		l.ScrollToBottom()
	}

	l.zones = l.zones[:0]
	budget := max(l.height, 0)
	lines := make([]string, 0, budget)
	idx := l.offsetIdx
	off := l.offsetLine
	row := 0
	for idx < len(l.view) && len(lines) < budget {
		itemLines := l.itemLines(idx)
		h := len(itemLines)
		gap := l.gapAfter(idx)

		if off >= 0 && off < h {
			if off == 0 && l.chevronDrawn(idx) {
				l.zones = append(l.zones, accordionZone{id: l.view[idx].id, row: row, width: l.accordion.Width()})
			}
			visible := itemLines[off:]
			if rem := budget - len(lines); len(visible) > rem {
				visible = visible[:rem]
			}
			lines = append(lines, visible...)
			row += len(visible)
			if gap > 0 {
				n := min(budget-len(lines), gap)
				for range n {
					lines = append(lines, "")
				}
				row += n
			}
		} else {
			// The offset starts inside the gap after this item.
			gapRemaining := gap - (off - h)
			if gapRemaining > 0 {
				n := min(budget-len(lines), gapRemaining)
				for range n {
					lines = append(lines, "")
				}
				row += n
			}
		}

		idx++
		off = 0
	}

	for len(lines) < budget {
		lines = append(lines, "")
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
		lines = append(lines, l.itemLines(idx)...)
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
	idx, line, follow := l.offsetIdx, l.offsetLine, l.follow
	l.ScrollBy(lines)
	return idx != l.offsetIdx || line != l.offsetLine || follow != l.follow
}

func buildPresentation(header string, blocks []agent.Block) []presentationItem {
	items := make([]presentationItem, 0, len(blocks)+1)
	if header != "" {
		items = append(items, presentationItem{
			id:    headerBlockID,
			rev:   headerRevision(header),
			group: headerGroupKey,
			start: headerSentinel,
			end:   headerSentinel,
		})
	}
	for i := 0; i < len(blocks); {
		if blocks[i].Kind == assistant.KindReasoning {
			j := i + 1
			for j < len(blocks) && blocks[j].Kind == assistant.KindReasoning {
				j++
			}
			items = append(items, newPresentationItem(blocks, reasoningGroupKey, i, j, nil))
			i = j
			continue
		}

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

package tui

import (
	"image"
	"strings"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/uax29/v2/words"
)

const (
	multiClickInterval  = 400 * time.Millisecond
	multiClickTolerance = 2
)

// selectionFrame is one rendered view of a terminal-sized document. Rows in
// documentRows are virtual document rows; a nil mapping means that screen and
// document rows have the same coordinates.
type selectionFrame struct {
	content       string
	width, height int
	documentRows  []int

	// floor is the first selectable document row. The startup header sits above
	// it, and its cells are decoration: Kitty placeholder runes on the image
	// path, which copy as garbage.
	floor int

	// split is the first screen row of the lower pane; the transcript shows
	// above it.
	split int
}

type selectionScope uint8

const (
	selectionScopeTranscript selectionScope = iota
	selectionScopeLower
)

// clamp keeps a pointer inside the pane a gesture started in.
func (frame selectionFrame) clamp(scope selectionScope, x, y int) (int, int) {
	last := max(0, frame.height-1)
	top, bottom := 0, max(0, frame.split-1)
	if scope == selectionScopeLower {
		top, bottom = min(frame.split, last), last
	}
	return x, max(top, min(bottom, y))
}

// selection owns one pane-scoped cell selection and its auto-scroll gesture.
// Transcript coordinates are virtual document rows; lower-pane coordinates
// are fixed screen rows.
type selection struct {
	anchor, focus image.Point
	pointer       image.Point
	clickPoint    image.Point
	scope         selectionScope
	clicks        clickTracker
	edge          int
	dragging      bool
	keepRange     bool
	singleCell    bool
	tickArmed     bool
	scrollToken   uint64
}

type clickTracker struct {
	lastAt time.Time
	point  image.Point
	scope  selectionScope
	count  int
}

func (c *clickTracker) next(now time.Time, point image.Point, scope selectionScope) int {
	elapsed := now.Sub(c.lastAt)
	if !c.lastAt.IsZero() && elapsed >= 0 && elapsed <= multiClickInterval &&
		c.scope == scope && c.point.Y == point.Y && abs(c.point.X-point.X) <= multiClickTolerance {
		c.count++
	} else {
		c.count = 1
	}
	if c.count == 3 {
		c.reset()
		return 3
	}
	c.lastAt = now
	c.point = point
	c.scope = scope
	return c.count
}

func (c *clickTracker) reset() { *c = clickTracker{} }

func (s *selection) beginGesture(frame selectionFrame, scope selectionScope, x, y int) {
	s.stopScroll()
	s.scope = scope
	s.pointer = image.Pt(x, y)
	x, y = frame.clamp(scope, x, y)
	s.begin(frame, x, y)
	s.edge = s.edgeForPointer(s.pointer.Y, frame.split)
}

func (s *selection) beginClick(frame selectionFrame, scope selectionScope, x, y int, onControl bool, now time.Time) {
	point := frame.point(x, y)
	s.clickPoint = point
	if onControl {
		// A control starts a normal gesture so dragging can still select text,
		// but repeated presses must not become word or line selections.
		s.clicks.reset()
		s.beginGesture(frame, scope, x, y)
		return
	}
	clickCount := s.clicks.next(now, point, scope)
	s.scope = scope
	s.pointer = image.Pt(x, y)
	switch clickCount {
	case 2:
		if anchor, focus, ok := wordRange(frame, point); ok {
			s.beginRange(anchor, focus)
			return
		}
	case 3:
		if frame.width > 0 && point.Y >= frame.floor {
			s.beginRange(image.Pt(0, point.Y), image.Pt(frame.width-1, point.Y))
			return
		}
	}
	s.beginGesture(frame, scope, x, y)
}

func (s *selection) extendGesture(frame selectionFrame, x, y int) {
	s.pointer = image.Pt(x, y)
	x, y = frame.clamp(s.scope, x, y)
	point := frame.point(x, y)
	if point != s.focus {
		s.keepRange = false
		s.clicks.reset()
	}
	s.extend(frame, x, y)
	s.edge = s.edgeForPointer(s.pointer.Y, frame.split)
}

func (s *selection) finishGesture(frame selectionFrame, x, y int, document selectionFrame) string {
	s.pointer = image.Pt(x, y)
	x, y = frame.clamp(s.scope, x, y)
	if frame.point(x, y) != s.clickPoint {
		s.clicks.reset()
		s.keepRange = false
	}
	text := ""
	if s.keepRange {
		text = s.finishCurrent(document)
	} else {
		text = s.finish(frame, x, y, document)
	}
	s.stopScroll()
	return text
}

func (s *selection) edgeForPointer(y, transcriptHeight int) int {
	if s.scope != selectionScopeTranscript || transcriptHeight <= 0 {
		return 0
	}
	if y >= transcriptHeight {
		return 1
	}
	if s.anchor == s.focus {
		return 0
	}
	if y <= 0 {
		return -1
	}
	if y >= transcriptHeight-1 {
		return 1
	}
	return 0
}

func (s *selection) armScroll() (uint64, bool) {
	if s.tickArmed || !s.dragging || s.edge == 0 {
		return 0, false
	}
	s.tickArmed = true
	return s.scrollToken, true
}

func (s *selection) consumeScrollTick(token uint64) (int, bool) {
	if token != s.scrollToken {
		return 0, false
	}
	s.tickArmed = false
	return s.edge, s.dragging && s.edge != 0
}

func (s *selection) stopScroll() {
	s.edge = 0
	s.tickArmed = false
	s.scrollToken++
}

// begin starts a selection at a screen-cell coordinate.
func (s *selection) begin(frame selectionFrame, x, y int) {
	p := frame.point(x, y)
	s.anchor = p
	s.focus = p
	s.dragging = true
	s.keepRange = false
	s.singleCell = false
}

func (s *selection) beginRange(anchor, focus image.Point) {
	s.stopScroll()
	s.anchor = anchor
	s.focus = focus
	s.dragging = true
	s.keepRange = true
	s.singleCell = anchor == focus
}

// extend moves the focus of an active selection to a screen-cell coordinate.
func (s *selection) extend(frame selectionFrame, x, y int) {
	if !s.dragging {
		return
	}
	point := frame.point(x, y)
	if point != s.focus {
		s.singleCell = false
	}
	s.focus = point
}

// finish extends and copies the selection from document. The document is
// intentionally interpreted with identity row mapping: it is the complete
// virtual document, not the currently visible frame.
func (s *selection) finish(frame selectionFrame, x, y int, document selectionFrame) string {
	s.extend(frame, x, y)
	return s.finishCurrent(document)
}

func (s *selection) finishCurrent(document selectionFrame) string {
	if !s.dragging {
		return ""
	}
	if s.anchor == s.focus && !s.singleCell {
		clicks := s.clicks
		s.clear()
		s.clicks = clicks
		return ""
	}

	text := extractSelection(document, s.anchor, s.focus)
	s.dragging = false
	s.keepRange = false
	return text
}

// clear removes the active selection.
func (s *selection) clear() {
	s.anchor = image.Point{}
	s.focus = image.Point{}
	s.pointer = image.Point{}
	s.clickPoint = image.Point{}
	s.dragging = false
	s.keepRange = false
	s.singleCell = false
	s.clicks.reset()
	s.stopScroll()
}

// selecting reports whether a mouse drag is active.
func (s *selection) selecting() bool { return s.dragging }

// selected reports whether a non-empty range should remain visible.
func (s *selection) selected() bool { return s.anchor != s.focus || s.singleCell }

// render paints the active selection over frame. The original frame content
// is returned byte-for-byte when no cells are selected.
func (s *selection) render(frame selectionFrame) string {
	if !s.selected() {
		return frame.content
	}

	buf := frame.buffer()
	lo, hi := orderedPoints(s.anchor, s.focus)
	for y := range frame.height {
		virtualY := frame.virtualRow(y)
		// A drag anchored in the header makes lo.Y negative, which rowSpan does
		// not reject, so unselectable rows are dropped here instead.
		if virtualY < frame.floor {
			continue
		}
		start, end, ok := rowSpan(lo, hi, virtualY, frame.width)
		if !ok {
			continue
		}
		line := buf.Lines[y]
		for x := start; x <= end; {
			unitStart, unitEnd := cellUnit(line, x, frame.width)
			if unitStart < 0 {
				unitStart = x
				unitEnd = x + 1
			}
			if unitEnd <= x {
				unitEnd = x + 1
			}
			if unitStart <= end && unitEnd-1 >= start {
				cell := &line[unitStart]
				cell.Style = uv.Style{Attrs: uv.AttrReverse}
			}
			x = unitEnd
		}
	}
	return buf.Render()
}

func extractSelection(frame selectionFrame, anchor, focus image.Point) string {
	if frame.width == 0 || frame.height == 0 {
		return ""
	}

	buf := frame.buffer()
	lo, hi := orderedPoints(anchor, focus)
	firstY := max(frame.floor, lo.Y)
	lastY := min(frame.height-1, hi.Y)
	if firstY > lastY {
		return ""
	}
	rows := make([]string, 0, lastY-firstY+1)
	for y := firstY; y <= lastY; y++ {
		start, end, ok := rowSpan(lo, hi, y, frame.width)
		if !ok {
			continue
		}
		rows = append(rows, extractRow(buf.Lines[y], start, end, y, frame))
	}
	return strings.Join(rows, "\n")
}

func extractRow(line uv.Line, start, end, y int, frame selectionFrame) string {
	var b strings.Builder
	for x := 0; x <= end; {
		unitStart, unitEnd := cellUnit(line, x, frame.width)
		if unitStart < 0 {
			unitStart = x
			unitEnd = x + 1
		}
		if unitEnd <= x {
			unitEnd = x + 1
		}
		if unitStart <= end && unitEnd-1 >= start {
			b.WriteString(line[unitStart].Content)
		}
		x = unitEnd
	}
	return strings.TrimRight(b.String(), " ")
}

func orderedPoints(a, b image.Point) (image.Point, image.Point) {
	if a.Y < b.Y || a.Y == b.Y && a.X <= b.X {
		return a, b
	}
	return b, a
}

// rowSpan returns the inclusive horizontal span for one virtual document row.
func rowSpan(lo, hi image.Point, y, width int) (int, int, bool) {
	if y < lo.Y || y > hi.Y || width <= 0 {
		return 0, 0, false
	}
	start, end := 0, width-1
	switch {
	case lo.Y == hi.Y:
		start, end = lo.X, hi.X
	case y == lo.Y:
		start = lo.X
	case y == hi.Y:
		end = hi.X
	}
	start = max(0, min(width-1, start))
	end = max(0, min(width-1, end))
	if start > end {
		return 0, 0, false
	}
	return start, end, true
}

func (frame selectionFrame) point(x, y int) image.Point {
	if frame.width > 0 {
		x = max(0, min(frame.width-1, x))
	} else {
		x = 0
	}
	if frame.height > 0 {
		y = max(0, min(frame.height-1, y))
	} else {
		y = 0
	}
	return image.Point{X: x, Y: frame.virtualRow(y)}
}

func (frame selectionFrame) virtualRow(y int) int {
	if y >= 0 && y < len(frame.documentRows) {
		return frame.documentRows[y]
	}
	return y
}

func (frame selectionFrame) buffer() uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(frame.width, frame.height)
	// Lip Gloss renders with grapheme width, so selection coordinates must use
	// the same width model for multi-code-point graphemes such as emoji.
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(frame.content).Draw(&buf, buf.Bounds())
	return buf
}

func wordRange(frame selectionFrame, point image.Point) (image.Point, image.Point, bool) {
	if point.Y < frame.floor || point.X < 0 || point.X >= frame.width {
		return image.Point{}, image.Point{}, false
	}
	screenY := -1
	for y := range frame.height {
		if frame.virtualRow(y) == point.Y {
			screenY = y
			break
		}
	}
	if screenY < 0 {
		return image.Point{}, image.Point{}, false
	}
	line := frame.buffer().Lines[screenY]
	type cellSpan struct {
		byteStart, byteEnd int
		cellStart, cellEnd int
	}
	spans := make([]cellSpan, 0, frame.width)
	var text strings.Builder
	clickedByte := -1
	for x := 0; x < frame.width; {
		start, end := cellUnit(line, x, frame.width)
		if start < 0 {
			start, end = x, x+1
		}
		content := line[start].Content
		if content == "" {
			content = " "
		}
		byteStart := text.Len()
		text.WriteString(content)
		span := cellSpan{byteStart: byteStart, byteEnd: text.Len(), cellStart: start, cellEnd: end}
		spans = append(spans, span)
		if point.X >= start && point.X < end {
			clickedByte = byteStart
		}
		x = end
	}
	if clickedByte < 0 {
		return image.Point{}, image.Point{}, false
	}
	iterator := words.FromString(text.String())
	for iterator.Next() {
		if clickedByte < iterator.Start() || clickedByte >= iterator.End() {
			continue
		}
		if strings.TrimSpace(iterator.Value()) == "" {
			return image.Point{}, image.Point{}, false
		}
		first, last := -1, -1
		for _, span := range spans {
			if span.byteEnd <= iterator.Start() || span.byteStart >= iterator.End() {
				continue
			}
			if first < 0 {
				first = span.cellStart
			}
			last = span.cellEnd - 1
		}
		if first >= 0 && last >= first {
			return image.Pt(first, point.Y), image.Pt(last, point.Y), true
		}
		return image.Point{}, image.Point{}, false
	}
	return image.Point{}, image.Point{}, false
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// cellUnit returns the logical cell range containing x. A zero-width cell is
// Ultraviolet's continuation cell for a wide grapheme; walking from the line's
// start makes selecting either display cell include the grapheme once.
func cellUnit(line uv.Line, x, width int) (int, int) {
	if x < 0 || x >= width || x >= len(line) {
		return -1, -1
	}
	if line[x].Width > 0 {
		return x, min(width, x+max(1, line[x].Width))
	}
	for start := x - 1; start >= 0; start-- {
		if line[start].Width > 0 {
			end := min(width, start+max(1, line[start].Width))
			if x < end {
				return start, end
			}
			break
		}
	}
	return -1, -1
}

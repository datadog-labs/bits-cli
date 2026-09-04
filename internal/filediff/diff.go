package filediff

import (
	"strconv"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
)

// LineKind is a syntax-neutral unified-diff line role.
type LineKind uint8

const (
	LineContext LineKind = iota + 1
	LineDelete
	LineAdd
	LineNoNewline
	LinePending
	LineOmitted
)

// DiffLine carries a structured line from a unified hunk. Content is logical
// terminal-safe line text: it has neither a diff prefix nor a line terminator.
type DiffLine struct {
	Kind      LineKind
	OldNumber int
	NewNumber int
	Content   string
}

// Hunk is a renderer-neutral copy of one go-udiff hunk.
type Hunk struct {
	FromLine int
	ToLine   int
	OldCount int
	NewCount int
	Lines    []DiffLine
}

// TextFormat records source details that are intentionally removed from line
// content before terminal rendering. It lets the renderer surface a BOM or
// line-ending-only change without embedding control characters.
type TextFormat struct {
	BOM        bool
	LineEnding string
}

// Diff is a copy of go-udiff's structured unified representation.
type Diff struct {
	From         string
	To           string
	BeforeFormat TextFormat
	AfterFormat  TextFormat
	Hunks        []Hunk
	Additions    int
	Deletions    int
}

// AllLines returns a flattened view for tests and simple consumers. Hunks
// remain the sole stored representation used by renderers.
func (d Diff) AllLines() []DiffLine {
	var lines []DiffLine
	for _, hunk := range d.Hunks {
		lines = append(lines, hunk.Lines...)
	}
	return lines
}

// LineCount returns the total number of diff lines across all hunks. It avoids
// the allocation of len(AllLines()) when only the count is needed.
func (d Diff) LineCount() int {
	n := 0
	for _, hunk := range d.Hunks {
		n += len(hunk.Lines)
	}
	return n
}

// Build constructs a model directly from go-udiff's structured API. It never
// formats and reparses unified text.
func Build(fromName, toName, before, after string) Diff {
	u, err := unified(fromName, toName, before, after)
	if err != nil {
		return Diff{From: fromName, To: toName}
	}
	return fromUnified(u, before, after)
}

// BuildWithDisplay constructs a local view and the complete raw unified diff
// text for the durable Display field, from one go-udiff calculation.
//
// NOTE: Editor mutations intentionally include the preimage in Display: a
// workspace-write approval covers reading the target and the durable history
// shows the exact applied diff.
// TODO: Add a sanitizer/redactor here or at the client-response boundary when
// product policy requires filtering sensitive workspace content.
// TODO: Define an explicit durable Display byte budget when payload sizing
// becomes a product concern.
func BuildWithDisplay(fromName, toName, before, after string) (Diff, string) {
	u, err := unified(fromName, toName, before, after)
	if err != nil {
		return Diff{From: fromName, To: toName}, ""
	}
	return fromUnified(u, before, after), strings.TrimSuffix(u.String(), "\n")
}

// ParseUnifiedDiff reconstructs a structured Diff from the raw unified diff
// text produced by BuildWithDisplay, so restored history renders through the
// same path as a live diff. BeforeFormat/AfterFormat are not encoded in unified
// text and are therefore absent (no format-change annotation on restore).
func ParseUnifiedDiff(text string) (Diff, bool) {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return Diff{}, false
	}
	var d Diff
	var hunk *Hunk
	var oldLine, newLine int
	for _, raw := range strings.Split(text, "\n") {
		switch {
		case hunk == nil && strings.HasPrefix(raw, "--- "):
			d.From = raw[len("--- "):]
		case hunk == nil && strings.HasPrefix(raw, "+++ "):
			d.To = raw[len("+++ "):]
		case strings.HasPrefix(raw, "@@"):
			from, to, ok := parseHunkHeader(raw)
			if !ok {
				return Diff{}, false
			}
			d.Hunks = append(d.Hunks, Hunk{FromLine: from, ToLine: to})
			hunk = &d.Hunks[len(d.Hunks)-1]
			oldLine, newLine = from, to
		case hunk == nil || raw == "":
			return Diff{}, false
		default:
			content := raw[1:]
			switch raw[0] {
			case ' ':
				hunk.Lines = append(hunk.Lines, DiffLine{Kind: LineContext, OldNumber: oldLine, NewNumber: newLine, Content: content})
				oldLine, newLine = oldLine+1, newLine+1
				hunk.OldCount, hunk.NewCount = hunk.OldCount+1, hunk.NewCount+1
			case '-':
				hunk.Lines = append(hunk.Lines, DiffLine{Kind: LineDelete, OldNumber: oldLine, Content: content})
				oldLine++
				hunk.OldCount++
				d.Deletions++
			case '+':
				hunk.Lines = append(hunk.Lines, DiffLine{Kind: LineAdd, NewNumber: newLine, Content: content})
				newLine++
				hunk.NewCount++
				d.Additions++
			case '\\':
				hunk.Lines = append(hunk.Lines, DiffLine{Kind: LineNoNewline, Content: "No newline at end of file"})
			default:
				return Diff{}, false
			}
		}
	}
	if len(d.Hunks) == 0 {
		return Diff{}, false
	}
	return d, true
}

func parseHunkHeader(raw string) (from, to int, ok bool) {
	haveFrom, haveTo := false, false
	for _, field := range strings.Fields(raw) {
		switch {
		case strings.HasPrefix(field, "-"):
			from, haveFrom = parseHunkStart(field[1:])
		case strings.HasPrefix(field, "+"):
			to, haveTo = parseHunkStart(field[1:])
		}
	}
	return from, to, haveFrom && haveTo
}

func parseHunkStart(field string) (int, bool) {
	if i := strings.IndexByte(field, ','); i >= 0 {
		field = field[:i]
	}
	n, err := strconv.Atoi(field)
	return n, err == nil
}

func unified(fromName, toName, before, after string) (udiff.UnifiedDiff, error) {
	return udiff.ToUnifiedDiff(fromName, toName, before, udiff.Lines(before, after), udiff.DefaultContextLines)
}

func fromUnified(u udiff.UnifiedDiff, before, after string) Diff {
	d := Diff{From: u.From, To: u.To, BeforeFormat: textFormat(before), AfterFormat: textFormat(after)}
	for _, sourceHunk := range u.Hunks {
		if sourceHunk == nil {
			continue
		}
		hunk := Hunk{FromLine: sourceHunk.FromLine, ToLine: sourceHunk.ToLine}
		oldLine, newLine := sourceHunk.FromLine, sourceHunk.ToLine
		for _, sourceLine := range sourceHunk.Lines {
			kind := lineKind(sourceLine.Kind)
			// go-udiff keeps the line terminator in Content. The renderer model
			// stores logical line text without it; whether the source had a final
			// terminator is represented by LineNoNewline below.
			line := DiffLine{Kind: kind, Content: LogicalLineContent(sourceLine.Content)}
			switch kind {
			case LineDelete:
				line.OldNumber = oldLine
				oldLine++
				hunk.OldCount++
				d.Deletions++
			case LineAdd:
				line.NewNumber = newLine
				newLine++
				hunk.NewCount++
				d.Additions++
			default:
				line.OldNumber, line.NewNumber = oldLine, newLine
				oldLine++
				newLine++
				hunk.OldCount++
				hunk.NewCount++
			}
			hunk.Lines = append(hunk.Lines, line)
		}
		if len(hunk.Lines) > 0 {
			d.Hunks = append(d.Hunks, hunk)
		}
	}
	appendNoNewlineMarkers(&d, before, after)
	return d
}

// LogicalLineContent removes one LF or CRLF terminator from a diff source
// line. A bare trailing CR is source content, not a line terminator, and is
// deliberately retained; renderers remain responsible for terminal safety.
func LogicalLineContent(value string) string {
	if !strings.HasSuffix(value, "\n") {
		return value
	}
	value = strings.TrimSuffix(value, "\n")
	return strings.TrimSuffix(value, "\r")
}

func textFormat(text string) TextFormat {
	format := TextFormat{BOM: strings.HasPrefix(text, "\ufeff")}
	body := strings.TrimPrefix(text, "\ufeff")
	ending, ok := LineEndingStyle(body)
	if !ok {
		format.LineEnding = "mixed"
		return format
	}
	if ending == "\r\n" {
		format.LineEnding = "crlf"
	} else {
		format.LineEnding = "lf"
	}
	return format
}

func lineKind(kind udiff.OpKind) LineKind {
	switch kind {
	case udiff.Delete:
		return LineDelete
	case udiff.Insert:
		return LineAdd
	default:
		return LineContext
	}
}

// appendNoNewlineMarkers derives markers from the source documents rather
// than parsing go-udiff's textual representation.
func appendNoNewlineMarkers(d *Diff, before, after string) {
	oldMissing := before != "" && !strings.HasSuffix(before, "\n")
	newMissing := after != "" && !strings.HasSuffix(after, "\n")
	oldLast := strings.Count(before, "\n") + btoi(before != "")
	newLast := strings.Count(after, "\n") + btoi(after != "")
	if !oldMissing && !newMissing {
		return
	}
	for hi := range d.Hunks {
		h := &d.Hunks[hi]
		for li := 0; li < len(h.Lines); li++ {
			line := &h.Lines[li]
			missing := (oldMissing && line.Kind == LineDelete && line.OldNumber == oldLast) ||
				(newMissing && line.Kind == LineAdd && line.NewNumber == newLast)
			if !missing && oldMissing && newMissing && line.Kind == LineContext && line.OldNumber == oldLast && line.NewNumber == newLast {
				missing = true
			}
			if !missing {
				continue
			}
			marker := DiffLine{Kind: LineNoNewline, Content: "No newline at end of file"}
			h.Lines = append(h.Lines[:li+1], append([]DiffLine{marker}, h.Lines[li+1:]...)...)
			li++
		}
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

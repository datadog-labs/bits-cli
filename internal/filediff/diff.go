package filediff

import (
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

// Hunk is the bounded, renderer-neutral copy of one go-udiff hunk.
type Hunk struct {
	FromLine int
	ToLine   int
	OldCount int
	NewCount int
	Lines    []DiffLine
	Omitted  int
}

// TextFormat records source details that are intentionally removed from line
// content before terminal rendering. It lets the renderer surface a BOM or
// line-ending-only change without embedding control characters.
type TextFormat struct {
	BOM        bool
	LineEnding string
}

// Diff is a bounded copy of go-udiff's structured unified representation.
type Diff struct {
	From         string
	To           string
	BeforeFormat TextFormat
	AfterFormat  TextFormat
	Hunks        []Hunk
	Additions    int
	Deletions    int
	Truncated    bool
	Omitted      int
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

// Build constructs a bounded model directly from go-udiff's structured API.
// It never formats and reparses unified text. A non-positive limit means
// unlimited.
func Build(fromName, toName, before, after string, limit int) Diff {
	u, err := unified(fromName, toName, before, after)
	if err != nil {
		return Diff{From: fromName, To: toName}
	}
	return fromUnified(u, before, after, limit)
}

// BuildWithDisplay constructs a bounded local view and the complete raw unified
// diff text for the durable Display field, from one go-udiff calculation.
// TODO: place an explicit durable Display resource limit here before exposing
// write_file diffs for arbitrarily large inputs.
func BuildWithDisplay(fromName, toName, before, after string, limit int) (Diff, string) {
	u, err := unified(fromName, toName, before, after)
	if err != nil {
		return Diff{From: fromName, To: toName}, ""
	}
	return fromUnified(u, before, after, limit), strings.TrimSuffix(u.String(), "\n")
}

func unified(fromName, toName, before, after string) (udiff.UnifiedDiff, error) {
	return udiff.ToUnifiedDiff(fromName, toName, before, udiff.Lines(before, after), udiff.DefaultContextLines)
}

func fromUnified(u udiff.UnifiedDiff, before, after string, limit int) Diff {
	d := Diff{From: u.From, To: u.To, BeforeFormat: textFormat(before), AfterFormat: textFormat(after)}
	used := 0
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
			if limit > 0 && used >= limit {
				d.Truncated = true
				d.Omitted++
				hunk.Omitted++
				continue
			}
			hunk.Lines = append(hunk.Lines, line)
			used++
		}
		if len(hunk.Lines) > 0 {
			d.Hunks = append(d.Hunks, hunk)
		}
	}
	appendNoNewlineMarkersBounded(&d, before, after, limit, &used)
	if d.Truncated {
		marker := DiffLine{Kind: LineOmitted, Content: "diff lines omitted"}
		if len(d.Hunks) > 0 {
			d.Hunks[len(d.Hunks)-1].Lines = append(d.Hunks[len(d.Hunks)-1].Lines, marker)
		}
	}
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
func appendNoNewlineMarkersBounded(d *Diff, before, after string, limit int, used *int) {
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
			if used != nil && limit > 0 && *used >= limit {
				d.Truncated = true
				d.Omitted++
				continue
			}
			if used != nil {
				*used = *used + 1
			}
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

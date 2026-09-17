// Package diffrender renders structured diffs for terminals.
package diffrender

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/filediff"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// hunkSeparator separates rendered hunks.
const hunkSeparator = "⋮"

const diffLeftMargin = "  "

// Options configures diff rendering.
type Options struct {
	// Path selects syntax highlighting.
	Path string
	// Width is the maximum row width.
	Width int
	// Style holds diff styles.
	Style styles.Diff
	// MaxLines bounds output; zero is unbounded.
	MaxLines int
	// Tail keeps the newest lines.
	Tail bool
}

// Render returns styled diff text without a trailing newline.
func Render(diff filediff.Diff, opts Options) string {
	if opts.MaxLines > 0 {
		diff = window(diff, opts.MaxLines, opts.Tail)
	}
	return strings.Join(renderLines(diff, opts), "\n")
}

func renderLines(diff filediff.Diff, opts Options) []string {
	digits := 1
	for _, hunk := range diff.Hunks {
		for _, line := range hunk.Lines {
			digits = max(digits, decimalWidth(line.OldNumber), decimalWidth(line.NewNumber))
		}
	}
	lines := make([]string, 0, diff.LineCount()+len(diff.Hunks))
	for i, hunk := range diff.Hunks {
		if i > 0 {
			lines = append(lines, opts.Style.Meta.Render(ansi.Truncate(diffLeftMargin+"  "+hunkSeparator, opts.Width, "…")))
		}
		for _, line := range hunk.Lines {
			lines = append(lines, renderLine(line, digits, opts))
		}
	}
	return lines
}

func renderLine(line filediff.DiffLine, digits int, opts Options) string {
	switch line.Kind {
	case filediff.LineNoNewline, filediff.LineOmitted:
		return opts.Style.Meta.Render(ansi.Truncate(diffLeftMargin+"  "+line.Content, opts.Width, "…"))
	default:
	}

	marker, lineStyle := " ", opts.Style.Context
	switch line.Kind {
	case filediff.LineAdd:
		marker, lineStyle = "+", opts.Style.Add
	case filediff.LineDelete:
		marker, lineStyle = "-", opts.Style.Del
	default:
	}

	lineNumber := line.NewNumber
	if line.Kind == filediff.LineDelete {
		lineNumber = line.OldNumber
	}
	// Keep source content in the same column for every diff row. Context rows
	// still need a blank marker column to match the " - " and " + " gutters.
	separator := "   "
	if marker != " " {
		separator = " " + marker + " "
	}
	gutter := lineStyle.Foreground(opts.Style.Gutter.GetForeground()).Render(diffLeftMargin + gutterNumber(lineNumber, digits) + separator)
	available := max(1, opts.Width-ansi.StringWidth(gutter))
	content := ansi.Truncate(HighlightLine(opts.Path, line.Content, opts.Style.SyntaxDark, lineStyle), available, "…")
	return gutter + content
}

func gutterNumber(n, digits int) string {
	if n > 0 {
		return fmt.Sprintf("%*d", digits, n)
	}
	return strings.Repeat(" ", digits)
}

func decimalWidth(n int) int {
	if n <= 0 {
		return 1
	}
	return len(strconv.Itoa(n))
}

// window returns a bounded copy of diff.
func window(diff filediff.Diff, limit int, tail bool) filediff.Diff {
	if limit <= 0 || diff.LineCount() <= limit {
		return diff
	}
	if tail {
		return tailWindow(diff, limit)
	}
	return headWindow(diff, limit)
}

func tailWindow(diff filediff.Diff, limit int) filediff.Diff {
	remaining := limit
	omitted := 0
	selected := make([]filediff.Hunk, 0, len(diff.Hunks))
	for i := len(diff.Hunks) - 1; i >= 0; i-- {
		hunk := diff.Hunks[i]
		if remaining == 0 {
			omitted += len(hunk.Lines)
			continue
		}
		start := max(0, len(hunk.Lines)-remaining)
		omitted += start
		trimmed := hunk
		trimmed.Lines = append([]filediff.DiffLine(nil), hunk.Lines[start:]...)
		selected = append(selected, trimmed)
		remaining -= len(trimmed.Lines)
	}
	slices.Reverse(selected)
	if omitted > 0 && len(selected) > 0 {
		marker := filediff.DiffLine{Kind: filediff.LineOmitted, Content: hiddenLabel(omitted, "earlier")}
		selected[0].Lines = append([]filediff.DiffLine{marker}, selected[0].Lines...)
	}
	diff.Hunks = selected
	return diff
}

func headWindow(diff filediff.Diff, limit int) filediff.Diff {
	remaining := limit
	omitted := 0
	selected := make([]filediff.Hunk, 0, len(diff.Hunks))
	for _, hunk := range diff.Hunks {
		if remaining == 0 {
			omitted += len(hunk.Lines)
			continue
		}
		end := min(len(hunk.Lines), remaining)
		omitted += len(hunk.Lines) - end
		trimmed := hunk
		trimmed.Lines = append([]filediff.DiffLine(nil), hunk.Lines[:end]...)
		selected = append(selected, trimmed)
		remaining -= end
	}
	if omitted > 0 && len(selected) > 0 {
		marker := filediff.DiffLine{Kind: filediff.LineOmitted, Content: hiddenLabel(omitted, "more")}
		last := &selected[len(selected)-1]
		last.Lines = append(last.Lines, marker)
	}
	diff.Hunks = selected
	return diff
}

func hiddenLabel(n int, position string) string {
	suffix := "lines"
	if n == 1 {
		suffix = "line"
	}
	return fmt.Sprintf("%d %s %s hidden", n, position, suffix)
}

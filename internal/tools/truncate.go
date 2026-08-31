package tools

import (
	"fmt"
	"strings"
)

const (
	maxReadLines = 2000
	maxReadBytes = 100 * 1024
)

type truncation struct {
	content   string
	truncated bool
	next      int // 1-based start line for continuation; 0 if not truncated
}

// truncateLines applies line and byte caps to lines and appends a continuation
// hint when truncated.
// startLine is the 1-based line number of lines[0] in the source file.
// totalLines is the total line count of the source file.
func truncateLines(lines []string, startLine, totalLines int) truncation {
	var b strings.Builder
	written := 0
	byteCount := 0

	for i, line := range lines {
		cost := len(line) + 1 // newline
		if written >= maxReadLines || byteCount+cost > maxReadBytes {
			if written == 0 {
				return truncation{
					content:   fmt.Sprintf("[Line %d exceeds the %d-byte read limit.]", startLine, maxReadBytes),
					truncated: true,
					next:      startLine + 1,
				}
			}
			next := startLine + i
			fmt.Fprintf(&b, "\n\n[Showing lines %d\u2013%d of %d. Use offset=%d to continue.]",
				startLine, startLine+written-1, totalLines, next)
			return truncation{content: b.String(), truncated: true, next: next}
		}
		if written > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		written++
		byteCount += cost
	}
	return truncation{content: b.String()}
}

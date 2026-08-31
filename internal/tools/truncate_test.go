package tools

import (
	"fmt"
	"strings"
	"testing"
)

func TestTruncateLines(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		startLine  int
		totalLines int
		wantTrunc  bool
		wantNext   int
		wantSnip   string
	}{
		{
			name:       "no truncation",
			lines:      repeatLines(10, "hello"),
			startLine:  1,
			totalLines: 10,
			wantTrunc:  false,
		},
		{
			name:       "line cap",
			lines:      repeatLines(maxReadLines+5, "x"),
			startLine:  1,
			totalLines: maxReadLines + 5,
			wantTrunc:  true,
			wantNext:   maxReadLines + 1,
			wantSnip:   fmt.Sprintf("Showing lines 1\u2013%d of %d", maxReadLines, maxReadLines+5),
		},
		{
			name:       "byte cap",
			lines:      repeatLines(5, strings.Repeat("a", maxReadBytes/3)),
			startLine:  1,
			totalLines: 5,
			wantTrunc:  true,
			wantSnip:   "offset=",
		},
		{
			name:       "first line exceeds byte limit",
			lines:      []string{strings.Repeat("a", maxReadBytes+1)},
			startLine:  3,
			totalLines: 10,
			wantTrunc:  true,
			wantNext:   4,
			wantSnip:   "offset=4",
		},
		{
			name:       "continuation hint line numbers with offset",
			lines:      repeatLines(maxReadLines+1, "x"),
			startLine:  501,
			totalLines: maxReadLines + 501,
			wantTrunc:  true,
			wantNext:   501 + maxReadLines,
			wantSnip:   fmt.Sprintf("Showing lines 501\u2013%d of %d", 500+maxReadLines, maxReadLines+501),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := truncateLines(tt.lines, tt.startLine, tt.totalLines)
			if r.truncated != tt.wantTrunc {
				t.Errorf("truncated = %v, want %v", r.truncated, tt.wantTrunc)
			}
			if tt.wantNext != 0 && r.next != tt.wantNext {
				t.Errorf("next = %d, want %d", r.next, tt.wantNext)
			}
			if tt.wantSnip != "" && !strings.Contains(r.content, tt.wantSnip) {
				t.Errorf("content missing %q:\n%s", tt.wantSnip, r.content)
			}
		})
	}
}

func repeatLines(n int, s string) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = s
	}
	return lines
}

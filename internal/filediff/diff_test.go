package filediff

import (
	"testing"
)

func TestBuildUnifiedDiffLines(t *testing.T) {
	diff := Build("a/f.txt", "b/f.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\n", 0)
	if diff.Additions != 1 || diff.Deletions != 1 {
		t.Fatalf("counts = +%d -%d, want +1 -1", diff.Additions, diff.Deletions)
	}
	var deletion, addition DiffLine
	for _, line := range diff.AllLines() {
		switch line.Kind {
		case LineDelete:
			deletion = line
		case LineAdd:
			addition = line
		case LineContext, LineNoNewline, LinePending, LineOmitted:
		}
	}
	if deletion.Content != "two" || deletion.OldNumber != 2 || deletion.NewNumber != 0 {
		t.Fatalf("deletion = %#v", deletion)
	}
	if addition.Content != "TWO" || addition.OldNumber != 0 || addition.NewNumber != 2 {
		t.Fatalf("addition = %#v", addition)
	}
}

func TestBuildWithDisplayRawUnifiedDiff(t *testing.T) {
	renderDiff, display := BuildWithDisplay("a/f.txt", "b/f.txt", "before\n", "after\n", 0)
	if len(renderDiff.Hunks) != 1 {
		t.Fatalf("render diff = %#v", renderDiff)
	}
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-before\n+after"
	if display != want {
		t.Fatalf("display = %q, want %q", display, want)
	}
}

func TestBuildPreservesStructuredHunksAndNoFinalNewline(t *testing.T) {
	diff := Build("a/f.txt", "b/f.txt", "old", "new", 0)
	if len(diff.Hunks) != 1 || len(diff.AllLines()) < 4 {
		t.Fatalf("structured diff = %#v", diff)
	}
	noNewline := 0
	for _, line := range diff.AllLines() {
		if line.Kind == LineNoNewline {
			noNewline++
		}
	}
	if noNewline != 2 {
		t.Fatalf("no-final-newline markers = %d, want 2", noNewline)
	}

	bounded := Build("a/f.txt", "b/f.txt", "old", "new", 3)
	if !bounded.Truncated || bounded.Omitted == 0 {
		t.Fatalf("bound = truncated=%v omitted=%d", bounded.Truncated, bounded.Omitted)
	}
}

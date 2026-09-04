package filediff

import (
	"testing"
)

func TestBuildUnifiedDiffLines(t *testing.T) {
	diff := Build("a/f.txt", "b/f.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\n")
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
	renderDiff, display := BuildWithDisplay("a/f.txt", "b/f.txt", "before\n", "after\n")
	if len(renderDiff.Hunks) != 1 {
		t.Fatalf("render diff = %#v", renderDiff)
	}
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -1 +1 @@\n-before\n+after"
	if display != want {
		t.Fatalf("display = %q, want %q", display, want)
	}
}

func TestLineCountMatchesAllLines(t *testing.T) {
	diff := Build("a/f.txt", "b/f.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\nfour\n")
	if got, want := diff.LineCount(), len(diff.AllLines()); got != want {
		t.Fatalf("LineCount() = %d, want %d", got, want)
	}
	if (Diff{}).LineCount() != 0 {
		t.Fatalf("empty diff LineCount() = %d, want 0", (Diff{}).LineCount())
	}
}

func TestParseUnifiedDiffRoundTrip(t *testing.T) {
	built, display := BuildWithDisplay("a/f.txt", "b/f.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\n")
	parsed, ok := ParseUnifiedDiff(display)
	if !ok {
		t.Fatal("ParseUnifiedDiff returned ok=false")
	}
	if parsed.From != "a/f.txt" || parsed.To != "b/f.txt" || parsed.Additions != built.Additions || parsed.Deletions != built.Deletions {
		t.Fatalf("parsed = %#v, built = %#v", parsed, built)
	}
	if len(parsed.Hunks) != 1 || parsed.Hunks[0].FromLine != built.Hunks[0].FromLine || parsed.Hunks[0].OldCount != built.Hunks[0].OldCount || parsed.Hunks[0].NewCount != built.Hunks[0].NewCount {
		t.Fatalf("parsed hunks = %#v, built hunks = %#v", parsed.Hunks, built.Hunks)
	}
	pl, bl := parsed.AllLines(), built.AllLines()
	if len(pl) != len(bl) {
		t.Fatalf("line counts parsed=%d built=%d", len(pl), len(bl))
	}
	for i := range bl {
		if pl[i] != bl[i] {
			t.Fatalf("line %d: parsed %#v != built %#v", i, pl[i], bl[i])
		}
	}
}

func TestParseUnifiedDiffRejectsNonDiff(t *testing.T) {
	for _, text := range []string{"", "not a diff", "--- a/f\n+++ b/f\n"} {
		if _, ok := ParseUnifiedDiff(text); ok {
			t.Fatalf("ParseUnifiedDiff(%q) = ok, want rejected", text)
		}
	}
}

func TestBuildPreservesStructuredHunksAndNoFinalNewline(t *testing.T) {
	diff := Build("a/f.txt", "b/f.txt", "old", "new")
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
}

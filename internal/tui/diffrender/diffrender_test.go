package diffrender

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/filediff"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestRenderShowsAddedAndDeletedContent(t *testing.T) {
	diff := filediff.Build("a/f.txt", "b/f.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\n")
	out := ansi.Strip(Render(diff, Options{Path: "f.txt", Width: 80}))
	for _, want := range []string{"- two", "+ TWO", "  one", "  three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDropsHunkHeaders(t *testing.T) {
	diff := filediff.Build("a/f.txt", "b/f.txt", "one\ntwo\nthree\n", "one\nTWO\nthree\n")
	out := ansi.Strip(Render(diff, Options{Path: "f.txt", Width: 80}))
	if strings.Contains(out, "@@") {
		t.Fatalf("render should not contain @@ hunk headers:\n%s", out)
	}
}

func TestRenderUsesOneLineNumberColumn(t *testing.T) {
	var before, after strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&before, "line-%d\n", i)
		if i == 42 {
			fmt.Fprintln(&after, "LINE-42")
			continue
		}
		fmt.Fprintf(&after, "line-%d\n", i)
	}

	diff := filediff.Build("a/f.txt", "b/f.txt", before.String(), after.String())
	out := ansi.Strip(Render(diff, Options{Path: "f.txt", Width: 80}))
	for _, want := range []string{"42 - line-42", "42 + LINE-42", "41   line-41", "43   line-43"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "42    -") {
		t.Fatalf("render retained separate old/new number columns:\n%s", out)
	}
}

func TestRenderAlignsTabIndentedContentAcrossDiffRows(t *testing.T) {
	diff := filediff.Diff{Hunks: []filediff.Hunk{{
		Lines: []filediff.DiffLine{
			{Kind: filediff.LineContext, NewNumber: 205, Content: "\tm.finishConversationOperation()"},
			{Kind: filediff.LineDelete, OldNumber: 208, Content: "\tm.usage = nil"},
			{Kind: filediff.LineAdd, NewNumber: 208, Content: "\tm.usage = msg.result.Usage"},
		},
	}}}

	lines := strings.Split(ansi.Strip(Render(diff, Options{Path: "conversation.go", Width: 80})), "\n")
	for _, line := range lines {
		if got, want := strings.Index(line, "m."), 10; got != want {
			t.Errorf("source content begins in column %d, want %d: %q", got+1, want+1, line)
		}
	}
}

func TestRenderHunkSeparatorBetweenHunks(t *testing.T) {
	before := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n"
	after := "X\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\nY\n"
	diff := filediff.Build("a/f.txt", "b/f.txt", before, after)
	if len(diff.Hunks) < 2 {
		t.Fatalf("expected multiple hunks, got %d", len(diff.Hunks))
	}
	out := ansi.Strip(Render(diff, Options{Path: "f.txt", Width: 80}))
	if !strings.Contains(out, hunkSeparator) {
		t.Fatalf("render missing hunk separator %q:\n%s", hunkSeparator, out)
	}
}

func TestRenderStaysSingleRowPerLine(t *testing.T) {
	before := "package p\n"
	after := "package p\n// a trailing comment\n"
	diff := filediff.Build("a/f.go", "b/f.go", before, after)
	out := Render(diff, Options{Path: "f.go", Width: 80})
	if got, want := len(strings.Split(out, "\n")), len(diff.AllLines()); got != want {
		t.Fatalf("rendered %d rows, want %d (one per diff line):\n%s", got, want, ansi.Strip(out))
	}
}

func TestRenderKeepsAddBackgroundAcrossStyledSpans(t *testing.T) {
	diff := filediff.Build("a/f.go", "b/f.go", "", "func main() {}\n")
	const background = "48;2;18;52;86"
	out := Render(diff, Options{
		Path:  "f.go",
		Width: 80,
		Style: styles.Diff{
			Add:        lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f0f0")).Background(lipgloss.Color("#123456")),
			Context:    lipgloss.NewStyle(),
			Gutter:     lipgloss.NewStyle().Foreground(lipgloss.Color("#999999")),
			SyntaxDark: true,
		},
	})
	if got := strings.Count(out, background); got < 3 {
		t.Fatalf("add background appears %d times, want gutter and syntax spans: %q", got, out)
	}
}

func TestRenderEscapesControlCharacters(t *testing.T) {
	diff := filediff.Build("a/f.txt", "b/f.txt", "x\n", "x\x1by\n")
	out := ansi.Strip(Render(diff, Options{Path: "f.txt", Width: 80}))
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("render leaked a raw ESC byte:\n%q", out)
	}
	if !strings.ContainsRune(out, '\u241b') {
		t.Fatalf("render should escape ESC to Control Picture \u241b:\n%q", out)
	}
}

func TestProcessValuePreservesBareCarriageReturn(t *testing.T) {
	if got, want := processValue("a\rb\n"), "a\u240db"; got != want {
		t.Errorf("processValue = %q, want %q", got, want)
	}
}

// changedDiff creates n modified lines.
func changedDiff(t *testing.T, n int) filediff.Diff {
	t.Helper()
	var before, after strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&before, "line-%d\n", i)
		fmt.Fprintf(&after, "LINE-%d\n", i)
	}
	return filediff.Build("a/f.txt", "b/f.txt", before.String(), after.String())
}

func TestWindowTailKeepsNewestLinesAndReportsHidden(t *testing.T) {
	diff := changedDiff(t, 40)

	out := ansi.Strip(Render(diff, Options{Path: "f.txt", Width: 80, MaxLines: 5, Tail: true}))
	if !strings.Contains(out, "LINE-40") {
		t.Fatalf("tail window should keep the newest changed line:\n%s", out)
	}
	if !strings.Contains(out, "hidden") {
		t.Fatalf("tail window should report hidden lines:\n%s", out)
	}
	rows := strings.Count(out, "\n") + 1
	if rows > 6 {
		t.Fatalf("tail window rendered %d rows, want <= 6 (5 lines + marker):\n%s", rows, out)
	}
}

func TestWindowUnboundedRendersEverything(t *testing.T) {
	diff := changedDiff(t, 30)

	out := ansi.Strip(Render(diff, Options{Path: "f.txt", Width: 80, MaxLines: 0}))
	if strings.Contains(out, "hidden") {
		t.Fatalf("unbounded render should not hide lines:\n%s", out)
	}
	if !strings.Contains(out, "LINE-1") || !strings.Contains(out, "LINE-30") {
		t.Fatalf("unbounded render should include first and last changes:\n%s", out)
	}
}

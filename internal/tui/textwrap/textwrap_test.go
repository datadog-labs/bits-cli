package textwrap

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func plainRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = ansi.Strip(row)
	}
	return out
}

func TestWrap(t *testing.T) {
	tests := []struct {
		name string
		text string
		o    Options
		want []string
	}{
		{
			name: "fits",
			text: "go test ./...",
			o:    Options{Width: 20},
			want: []string{"go test ./..."},
		},
		{
			name: "words drop the space at a soft break",
			text: "alpha beta gamma delta",
			o:    Options{Width: 11},
			want: []string{"alpha beta", "gamma delta"},
		},
		{
			name: "prefixes count toward the width",
			text: "alpha beta gamma",
			o:    Options{Width: 12, FirstPrefix: "• run ", RestPrefix: "      "},
			want: []string{"• run alpha", "      beta", "      gamma"},
		},
		{
			name: "a prefix leaving no room is cut",
			text: "ab",
			o:    Options{Width: 4, FirstPrefix: "• run ", RestPrefix: "      "},
			want: []string{"• ra", "   b"},
		},
		{
			name: "first and rest rooms differ",
			text: "aa bb cc dd ee",
			o:    Options{Width: 9, FirstPrefix: "> ", RestPrefix: ">>>>> "},
			want: []string{"> aa bb", ">>>>> cc", ">>>>> dd", ">>>>> ee"},
		},
		{
			name: "breakpoints allow breaks inside a token",
			text: "internal/tui/chat/tool.go",
			o:    Options{Width: 14, Breakpoints: "/"},
			want: []string{"internal/tui/", "chat/tool.go"},
		},
		{
			name: "without breakpoints a long token hard-wraps",
			text: "echo abcdefghijkl",
			o:    Options{Width: 5},
			want: []string{"echo", "abcde", "fghij", "kl"},
		},
		{
			name: "newlines start rows",
			text: "one\n\ntwo",
			o:    Options{Width: 10},
			want: []string{"one", "", "two"},
		},
		{
			name: "indentation repeats on every row",
			text: "    print('abcdefghij')",
			o:    Options{Width: 16, KeepIndent: true},
			want: []string{"    print('abcde", "    fghij')"},
		},
		{
			name: "without KeepIndent leading spaces are content",
			text: "  ab cd",
			o:    Options{Width: 5},
			want: []string{"  ab", "cd"},
		},
		{
			name: "non-breaking spaces bind words",
			text: "a b\u00a0c",
			o:    Options{Width: 3},
			want: []string{"a", "b\u00a0c"},
		},
		{
			name: "wide graphemes measure two cells",
			text: "日本語 abc",
			o:    Options{Width: 4},
			want: []string{"日本", "語", "abc"},
		},
		{
			name: "whitespace-only line keeps its spaces",
			text: "   ",
			o:    Options{Width: 10},
			want: []string{"   "},
		},
		{
			name: "empty text is one empty row",
			text: "",
			o:    Options{Width: 10, FirstPrefix: "> "},
			want: []string{"> "},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := plainRows(Wrap(test.o, Plain(test.text)))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Wrap(%q) =\n%q\nwant\n%q", test.text, got, test.want)
			}
		})
	}
}

func TestWrapOverflowingBreakpointsStayInWidth(t *testing.T) {
	// ansi.Wrap lets "&&" overflow the row here; Wrap must not.
	rows := plainRows(Wrap(Options{Width: 14, Breakpoints: "/|;&"}, Plain("go test ./... && go vet ./...")))
	for _, row := range rows {
		if ansi.StringWidth(row) > 14 {
			t.Fatalf("row %q exceeds 14 cells in %q", row, rows)
		}
	}
	if got := strings.Join(rows, " "); !strings.Contains(got, "&&") {
		t.Fatalf("operators lost: %q", rows)
	}
}

func TestWrapEllipsisCutsOverlongWords(t *testing.T) {
	for _, test := range []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{"fits", "ab cd", 5, []string{"ab cd"}},
		{"own row", "x abcdefgh y", 5, []string{"x", "abcd…", "y"}},
		{"first word", "abcdefgh", 4, []string{"abc…"}},
		{"wide grapheme", "界界界 a", 5, []string{"界界…", "a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := plainRows(Wrap(Options{Width: test.width, Ellipsis: "…"}, Plain(test.text)))
			if !slices.Equal(got, test.want) {
				t.Fatalf("rows = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWrapAvoidBreak(t *testing.T) {
	avoid := func(text string) Segment { return Segment{Text: text, AvoidBreak: true} }
	for _, test := range []struct {
		name  string
		segs  []Segment
		width int
		want  []string
	}{
		{"moves whole", []Segment{Plain("f("), avoid(`"a b c"`), Plain(", x)")}, 8, []string{"f(", `"a b c",`, "x)"}},
		{"adjacent text wraps separately", []Segment{Plain("f("), avoid(`"123456"`), Plain(")")}, 8, []string{"f(", `"123456"`, ")"}},
		{"breakpoints suspended", []Segment{Plain("rg -e "), avoid("'a|b|c'")}, 10, []string{"rg -e", "'a|b|c'"}},
		{"too long breaks as usual", []Segment{Plain("f("), avoid(`"alpha beta gamma"`)}, 10, []string{`f("alpha`, "beta", `gamma"`}},
		{"plain unaffected", []Segment{Plain(`f("a b c", x)`)}, 8, []string{`f("a b`, `c", x)`}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := plainRows(Wrap(Options{Width: test.width, Breakpoints: "|"}, test.segs...))
			if !slices.Equal(got, test.want) {
				t.Fatalf("rows = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWrapCapsIndentation(t *testing.T) {
	rows := plainRows(Wrap(Options{Width: 10, KeepIndent: true}, Plain("            deep words")))
	for _, row := range rows {
		if indent := len(row) - len(strings.TrimLeft(row, " ")); indent > 5 && strings.TrimSpace(row) != "" {
			t.Fatalf("indent %d exceeds half the room in %q", indent, rows)
		}
	}
	checkProperties(t, []Segment{Plain("            deep words")}, Options{Width: 10, KeepIndent: true})
}

func TestWrapReappliesStylesOnEveryRow(t *testing.T) {
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	blue := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	segs := []Segment{Styled("red words that wrap", red), Plain(" plain "), Styled("bold blue tail", blue)}
	rows := Wrap(Options{Width: 8}, segs...)
	if len(rows) < 3 {
		t.Fatalf("expected several rows, got %q", rows)
	}
	for _, row := range rows {
		text := ansi.Strip(row)
		styled := strings.Contains(text, "red") || strings.Contains(text, "wrap") || strings.Contains(text, "blue") || strings.Contains(text, "tail")
		if styled && !strings.HasPrefix(row, "\x1b[") {
			t.Fatalf("styled row %q does not open its own style", row)
		}
		if strings.Count(row, "\x1b[") > 0 && !strings.HasSuffix(row, ansi.ResetStyle) {
			t.Fatalf("row %q leaks its style into the next", row)
		}
	}
}

func TestWrapRendersSegmentsWithTheirStyles(t *testing.T) {
	rows := Wrap(Options{Width: 20}, Styled("run", tag("A")), Plain(" "), Styled("ls", tag("B")))
	if want := []string{"<A>run</A> <B>ls</B>"}; !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
	rows = Wrap(Options{Width: 6, RestPrefix: "  "}, Styled("alpha beta", tag("A")))
	if want := []string{"<A>alpha</A>", "  <A>beta</A>"}; !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
}

func TestWrapStylesKeptIndent(t *testing.T) {
	rows := Wrap(Options{Width: 10, KeepIndent: true}, Styled("    alpha beta", tag("A")))
	want := []string{"<A>    </A><A>alpha</A>", "<A>    </A><A>beta</A>"}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
}

func TestRender(t *testing.T) {
	if got, want := Render(Styled("a b", tag("A")), Plain(" "), Styled("c", tag("B"))), "<A>a b</A> <B>c</B>"; got != want {
		t.Fatalf("Render = %q, want %q", got, want)
	}
}

// tag is a test style that makes segment boundaries visible.
type tag string

func (s tag) Render(strs ...string) string {
	return "<" + string(s) + ">" + strings.Join(strs, " ") + "</" + string(s) + ">"
}

// checkProperties asserts the package guarantees for one layout.
func checkProperties(t *testing.T, segs []Segment, o Options) {
	t.Helper()
	rows := Wrap(o, segs...)
	width := max(1, o.Width)
	first, rest := fitPrefix(o.FirstPrefix, width), fitPrefix(o.RestPrefix, width)
	prefixWidth := max(ansi.StringWidth(first), ansi.StringWidth(rest))
	widest := 0
	for _, seg := range segs {
		for text := seg.Text; text != ""; {
			cluster, w := ansi.FirstGraphemeCluster(text, ansi.GraphemeWidth)
			text = text[len(cluster):]
			widest = max(widest, w)
		}
	}
	for _, row := range rows {
		if w := ansi.StringWidth(row); w > width && prefixWidth+widest <= width {
			t.Fatalf("row %q is %d cells, over width %d; rows:\n%q", ansi.Strip(row), w, width, plainRows(rows))
		}
	}
	var in strings.Builder
	for _, seg := range segs {
		in.WriteString(seg.Text)
	}
	var out strings.Builder
	for i, row := range plainRows(rows) {
		prefix := ansi.Strip(rest)
		if i == 0 {
			prefix = ansi.Strip(first)
		}
		if !strings.HasPrefix(row, prefix) {
			t.Fatalf("row %q lacks its prefix %q", row, prefix)
		}
		out.WriteString(strings.TrimPrefix(row, prefix))
	}
	if got, want := nonSpace(out.String()), nonSpace(in.String()); got != want {
		t.Fatalf("text not preserved:\n got %q\nwant %q", got, want)
	}
}

func nonSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) && r != '\u00a0' {
			return -1
		}
		return r
	}, s)
}

func FuzzWrap(f *testing.F) {
	seeds := []string{
		"go test ./... && go vet ./...",
		"rg -n -g '*.go' -e 'a|b|c' internal/tui | sort -t: -k1,1 | head -n 40",
		"python3 - <<'PY'\nif x:\n        print('abcdefghij')\nPY",
		"x -- y --long-flag=a-b-c-d-e-f",
		"日本語のコマンド 🚀🔥✨ café-naïve",
		"echo " + strings.Repeat("x", 120),
		"    nested   spaces   inside\n\n   \n",
		"a\u00a0b c\u00a0d",
	}
	for _, seed := range seeds {
		for _, width := range []int{1, 3, 8, 14, 20, 33, 80} {
			f.Add(seed, width, true)
			f.Add(seed, width, false)
		}
	}
	f.Fuzz(func(t *testing.T, text string, width int, keepIndent bool) {
		if width < 1 || width > 200 || !isPrintable(text) {
			t.Skip()
		}
		checkProperties(t, []Segment{Plain(text)}, Options{Width: width, KeepIndent: keepIndent, Breakpoints: "/|;-"})
		checkProperties(t, []Segment{Styled(text, lipgloss.NewStyle().Foreground(lipgloss.Color("1")))}, Options{Width: width, FirstPrefix: "✓ ran ", RestPrefix: "  │   ", KeepIndent: keepIndent})
		half := len(text) / 2
		for half > 0 && half < len(text) && !utf8.RuneStart(text[half]) {
			half--
		}
		checkProperties(t, []Segment{Plain(text[:half]), {Text: text[half:], AvoidBreak: true}}, Options{Width: width, KeepIndent: keepIndent, Breakpoints: "/|;-"})
		for _, row := range Wrap(Options{Width: width, KeepIndent: keepIndent, Ellipsis: "…"}, Plain(text)) {
			if w := ansi.StringWidth(row); w > width && width > 1 {
				t.Fatalf("ellipsis row %q is %d cells, over width %d", row, w, width)
			}
		}
	})
}

// isPrintable rejects control characters other than newlines; callers
// escape those before rendering.
func isPrintable(s string) bool {
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if r == utf8.RuneError || unicode.IsControl(r) || r == '\t' {
			return false
		}
	}
	return true
}

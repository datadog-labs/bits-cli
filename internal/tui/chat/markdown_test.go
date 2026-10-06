package chat

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

var streamedMarkdownCorpus = []string{
	"# Title\n\nIntro with **bold**, `code`, and a [link](https://example.com).\n\n" +
		"## Section\n\nSetext heading\n===\n\nAnother\n---\n\n" +
		"- one\n- two\n  - nested\n\n- loose after a blank line\n\n" +
		"1. first\n2. second\n\n   continued item paragraph\n\n3) other list\n\n" +
		"7. starts at seven\n8. eight\n\n" +
		"- [ ] task\n- [x] done\n\n" +
		"> quote\n> - list in quote\nlazy continuation\n\n> second quote\n\n" +
		"| a | b |\n| --- | ---: |\n| 1 | 2 |\n\n" +
		"```go\nfunc main() {\n\n\tfmt.Println(\"blank line above\")\n}\n```\n\n" +
		"~~~\ntilde fence\n~~~\n\n" +
		"    indented code\n\n    more indented code\n\n" +
		"---\n\n* * *\n\n" +
		"Term\n: definition\n\nOther term\n\n: loose definition\n\n" +
		"<div>\nhtml block\n</div>\n\n<!-- comment -->\n\nParagraph after html.\n\n" +
		"Hard  \nbreak and ~~strike~~ and https://autolink.example.com.\n\n" +
		"para\n#not a heading\n\n#\n\nEnd.\n",
	"| service | p95 |\n| --- | ---: |\n| web | 42ms |\n| api | 7ms |\n\n" +
		"Unfinished table follows.\n\n| x | y |\n",
	"Text then a fence\n\n```\nno closing fence\n\nstill code\n",
	"1. a\n\n   ```\n   code in item\n\n   more\n   ```\n\nafter\n\n- b\n\n      indented in item\n\nend\n",
	generatedMarkdown(4),
}

func generatedMarkdown(sections int) string {
	words := []string{"latency", "service", "p95", "deploy", "rollback", "cluster", "trace", "span"}
	sentence := func(i, n int) string {
		ws := make([]string, n)
		for k := range ws {
			ws[k] = words[(i*7+k*3)%len(words)]
		}
		ws[1] = "**" + ws[1] + "**"
		ws[3] = "`" + ws[3] + "`"
		ws[5] = "[" + ws[5] + "](https://docs.datadoghq.com)"
		return strings.Join(ws, " ") + "."
	}
	parts := []string{"# Generated"}
	for i := range sections {
		var rows []string
		for r := range 3 {
			rows = append(rows, fmt.Sprintf("| svc-%d-%d | %dms |", i, r, 10+r))
		}
		parts = append(parts,
			fmt.Sprintf("## %d. %s", i+1, sentence(i, 6)),
			sentence(i, 14)+" "+sentence(i+1, 14),
			"- "+sentence(i, 6)+"\n  - nested\n- "+sentence(i+2, 6),
			"1. "+sentence(i*3, 8)+"\n2. "+sentence(i*3+1, 8),
			"> "+sentence(i+9, 12),
			"| service | p50 |\n| --- | ---: |\n"+strings.Join(rows, "\n"),
			"```go\nfunc main() {\n\tfmt.Println(\"p95\")\n}\n```",
			"---",
		)
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func TestMarkdownRendererStreamingMatchesFullRender(t *testing.T) {
	t.Parallel()
	const width = 60
	for _, dark := range []bool{true, false} {
		style := markdownStyleConfig(dark)
		for i, doc := range streamedMarkdownCorpus {
			for _, step := range []int{1, 17, 131} {
				if step == 1 && (!dark || len(doc) > 2000) {
					continue
				}
				t.Run(fmt.Sprintf("dark=%v/doc%d/step%d", dark, i, step), func(t *testing.T) {
					t.Parallel()
					var streamed markdownRenderer
					settled := false
					for n := step; ; n += step {
						n = min(n, len(doc))
						src := doc[:n]
						got := streamed.Render(src, width, style)
						want := renderMarkdown(src, width, style)
						if g, w := markdownCells(got, width), markdownCells(want, width); g != w {
							t.Fatalf("streamed rendering of %d bytes differs from a full render\nsource tail: %q\n got:\n%s\nwant:\n%s",
								n, src[max(0, n-80):], g, w)
						}
						settled = settled || streamed.prefix.src != ""
						if n == len(doc) {
							break
						}
					}
					if !settled && strings.Count(doc, "\n\n") > openMarkdownBlocks+1 {
						t.Fatal("streaming never settled a prefix")
					}
				})
			}
		}
	}
}

func markdownCells(s string, width int) string {
	var b strings.Builder
	for y, line := range strings.Split(s, "\n") {
		buf := uv.NewScreenBuffer(width+8, 1)
		uv.NewStyledString(line).Draw(buf, buf.Bounds())
		var row strings.Builder
		for x := range width + 8 {
			c := buf.CellAt(x, 0)
			switch {
			case c == nil || c.Width == 0:
			case strings.TrimSpace(c.Content) == "" && c.Style.Bg == nil &&
				c.Style.Underline == 0 && c.Style.Attrs&(uv.AttrReverse|uv.AttrStrikethrough) == 0:
				row.WriteString(" ")
			default:
				row.WriteString(c.Style.Styled(c.Content))
			}
		}
		fmt.Fprintf(&b, "%3d|%s\n", y, strings.TrimRight(row.String(), " "))
	}
	return b.String()
}

func TestSettledMarkdown(t *testing.T) {
	for _, test := range []struct {
		name, src, settled string
	}{
		{"too few blocks", "# a\n\nb\n", ""},
		{"keeps two open", "# a\n\nb\n\nc\n", "# a\n\n"},
		{"partial line not parsed", "# a\n\nb\n\nc\n\n#", "# a\n\n"},
		{"partial heading marker", "# a\n\nb\n#", ""},
		{"fence with blank lines", "a\n\n```\nx\n\ny\n\nz\n", ""},
		{"closed fence", "a\n\n```\nx\n\ny\n```\n\nb\n\nc\n", "a\n\n```\nx\n\ny\n```\n\n"},
		{"loose list is one block", "- a\n\n- b\n\nc\n\nd\n", "- a\n\n- b\n\n"},
		{"definition list starts at its term", "p\n\nTerm\n: def\n\nq\n\nr\n", "p\n\nTerm\n: def\n\n"},
		{"html block stays with its neighbours", "a\n\n<div>\nx\n</div>\n\nb\n\nc\n\nd\n", "a\n\n<div>\nx\n</div>\n\nb\n\n"},
		{"indented block", "a\n\n    code\n\nb\n\nc\n", "a\n\n    code\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, _ := settledMarkdown(test.src)
			if got := test.src[:n]; got != test.settled {
				t.Fatalf("settled %q, want %q", got, test.settled)
			}
		})
	}
}

func TestMarkdownRendererResetsPrefixForUnrelatedSource(t *testing.T) {
	style := markdownStyleConfig(true)
	doc := generatedMarkdown(3)
	var r markdownRenderer
	r.Render(doc, 60, style)
	if r.prefix.src == "" {
		t.Fatal("rendering a multi-block document settled nothing")
	}
	other := "# Other\n\nunrelated\n"
	if got, want := r.Render(other, 60, style), renderMarkdown(other, 60, style); got != want {
		t.Fatalf("unrelated source reused a stale prefix\n got=%q\nwant=%q", got, want)
	}

	withRef := doc + "\n[docs]: https://example.com\n"
	if got, want := r.Render(withRef, 60, style), renderMarkdown(withRef, 60, style); got != want {
		t.Fatalf("reference definition rendered from parts\n got=%q\nwant=%q", got, want)
	}
	if r.prefix.src != "" {
		t.Fatal("a document with reference definitions kept a settled prefix")
	}
}

func TestRenderMarkdownMatchesInternal(t *testing.T) {
	src := "# Title\n\nHello **world**."
	got := RenderMarkdown(src, 40, true)
	want := renderMarkdown(src, 40, markdownStyleConfig(true))
	if got != want {
		t.Fatalf("RenderMarkdown != internal renderMarkdown\n got=%q\nwant=%q", got, want)
	}
	if !strings.Contains(got, "Title") {
		t.Fatalf("expected heading text in output, got %q", got)
	}
}

func TestMarkdownRendererReusesSetupForWidthAndTheme(t *testing.T) {
	dark := markdownStyleConfig(true)
	light := markdownStyleConfig(false)
	var r markdownRenderer
	r.Render("one", 80, dark)
	first := r.term
	r.Render("two", 80, dark)
	if first == nil || r.term != first {
		t.Fatal("unchanged width/theme rebuilt the Markdown renderer")
	}

	r.Render("wide", 100, dark)
	wide := r.term
	if wide == first {
		t.Fatal("width change reused a renderer with stale wrapping")
	}
	r.Render("light", 100, light)
	if r.term == wide {
		t.Fatal("theme change reused a renderer with stale styles")
	}

	r.Render("| A | B |\n|---|---|\n| one | two |", 100, light)
	var fresh markdownRenderer
	const plain = "plain text after a structured block"
	if got, want := r.Render(plain, 100, light), fresh.Render(plain, 100, light); got != want {
		t.Fatalf("reused renderer output = %q, want fresh output %q", got, want)
	}
}

func TestMarkdownRendererPreservesSourceSemantics(t *testing.T) {
	style := markdownStyleConfig(true)
	for _, test := range []struct {
		name, input, equivalent string
	}{
		// A tab at the end of a source line is not Markdown's two-space hard
		// break. Replacing it with four spaces before parsing would add one.
		{"tab", "first\t\nsecond", "first\nsecond"},
		// CRLF is one line ending. Replacing CR with a space would turn the
		// existing trailing space into a two-space hard break.
		{"crlf", "first \r\nsecond", "first \nsecond"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ansi.Strip(renderMarkdown(test.input, 80, style))
			want := ansi.Strip(renderMarkdown(test.equivalent, 80, style))
			if got != want {
				t.Fatalf("input changed Markdown parsing\n got=%q\nwant=%q", got, want)
			}
			if strings.Contains(got, "\t") || strings.Contains(got, "\r") {
				t.Fatalf("rendered Markdown retained a layout control: %q", got)
			}
		})
	}
}

func TestMarkdownRendererEscapesControlsBeforeRendering(t *testing.T) {
	rendered := renderMarkdown("safe\x1b]0;unsafe\x07\rtext\u009b31m", 80, markdownStyleConfig(true))
	for _, control := range []string{"\x1b]0;unsafe", "\x07", "\r", "\u009b"} {
		if strings.Contains(rendered, control) {
			t.Fatalf("rendered Markdown retained source control %q: %q", control, rendered)
		}
	}
	plain := ansi.Strip(rendered)
	for _, want := range []string{"␛]0;unsafe␇", "␍", `\u009B`} {
		if !strings.Contains(plain, want) {
			t.Errorf("rendered Markdown missing %q: %q", want, plain)
		}
	}

	rendered = renderMarkdown("tab: &Tab; escape: &#27;]0;unsafe bell: &#7;", 80, markdownStyleConfig(true))
	for _, control := range []string{"\t", "\x1b]0;unsafe", "\x07"} {
		if strings.Contains(rendered, control) {
			t.Fatalf("rendered Markdown decoded source control %q: %q", control, rendered)
		}
	}
}

func TestMarkdownRendererKeepsTableEmailAutolinksInTheirCells(t *testing.T) {
	const source = "| User | Views |\n|---|---|\n| <ada@example.com> | 42 |"
	plain := ansi.Strip(renderMarkdown(source, 80, markdownStyleConfig(true)))

	if strings.Contains(plain, "[1]:") {
		t.Fatalf("table link rendered as a footnote instead of inline: %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "ada@example.com") && strings.Contains(line, "42") {
			return
		}
	}
	t.Fatalf("table link and its value did not share a row: %q", plain)
}

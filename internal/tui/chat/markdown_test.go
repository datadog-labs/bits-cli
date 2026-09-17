package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

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

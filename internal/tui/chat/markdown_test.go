package chat

import (
	"strings"
	"testing"
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

package chat

import (
	"strings"
	"testing"
)

func TestRenderMarkdownMatchesInternal(t *testing.T) {
	src := "# Title\n\nHello **world**."
	got := RenderMarkdown(src, 40, true)
	want := renderMarkdown(src, 40, markdownStyleName(true))
	if got != want {
		t.Fatalf("RenderMarkdown != internal renderMarkdown\n got=%q\nwant=%q", got, want)
	}
	if !strings.Contains(got, "Title") {
		t.Fatalf("expected heading text in output, got %q", got)
	}
}

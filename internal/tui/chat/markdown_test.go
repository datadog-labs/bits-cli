package chat

import (
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/assistant"
)

// assistant builds an assistant text item for markdown rendering tests.
func assistantItem(text string) Item {
	return Item{Kind: assistant.KindText, Role: assistant.RoleAssistant, Text: text}
}

func TestMarkdown_FencedCodeBlockKeepsContent(t *testing.T) {
	it := assistantItem("```go\nfmt.Println(\"hi\")\n```")
	got := plain(it.Render(80, DefaultStyles(true)))
	if !strings.Contains(got, "fmt.Println") {
		t.Errorf("code block content missing: %q", got)
	}
}

func TestMarkdown_ListKeepsItems(t *testing.T) {
	it := assistantItem("- first\n- second\n- third")
	got := plain(it.Render(80, DefaultStyles(true)))
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(got, want) {
			t.Errorf("list item %q missing from %q", want, got)
		}
	}
}

func TestMarkdown_InlineCodeKeepsContent(t *testing.T) {
	it := assistantItem("use the `render` helper")
	got := plain(it.Render(80, DefaultStyles(true)))
	if !strings.Contains(got, "render") {
		t.Errorf("inline code content missing: %q", got)
	}
}

func TestMarkdown_LongParagraphWrapsWithinWidth(t *testing.T) {
	it := assistantItem(strings.Repeat("word ", 40))
	got := plain(it.Render(20, DefaultStyles(true)))
	if lines := strings.Count(got, "\n") + 1; lines < 2 {
		t.Fatalf("expected wrap into >=2 lines, got %d: %q", lines, got)
	}
	for _, ln := range strings.Split(got, "\n") {
		if w := ansi.StringWidth(ln); w > 20 {
			t.Errorf("line exceeds width 20: %q (%d)", ln, w)
		}
	}
}

func TestMarkdown_WidthChangeReRenders(t *testing.T) {
	it := assistantItem(strings.Repeat("word ", 40))
	narrow := plain(it.Render(20, DefaultStyles(true)))
	wide := plain(it.Render(80, DefaultStyles(true)))
	if narrow == wide {
		t.Errorf("expected different layout at different widths")
	}
}

func TestMarkdown_PartialFenceRendersWithoutError(t *testing.T) {
	// A streaming delta can leave an unclosed code fence; glamour tolerates it.
	it := assistantItem("```go\nfmt.Println(\"hi\")")
	got := plain(it.Render(80, DefaultStyles(true)))
	if !strings.Contains(got, "fmt.Println") {
		t.Errorf("partial fence dropped content: %q", got)
	}
}

func TestMarkdown_DatadogAccentDiffersFromStockStyle(t *testing.T) {
	const src = "# Heading\n\na [link](https://dtdg.co)"
	branded := renderMarkdown(src, 80, styles.DarkStyle)

	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(styles.DarkStyle),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		t.Fatalf("stock renderer: %v", err)
	}
	stockOut, err := r.Render(src)
	if err != nil {
		t.Fatalf("stock render: %v", err)
	}
	stock := strings.Trim(stockOut, "\n")

	if branded == stock {
		t.Error("expected Datadog-accented markdown to differ from the stock dark style")
	}
	if !strings.Contains(plain(branded), "Heading") {
		t.Errorf("heading content lost: %q", branded)
	}
}

func TestMarkdown_StyleAdaptsToBackground(t *testing.T) {
	it := assistantItem("**bold** text")
	dark := it.Render(80, DefaultStyles(true))
	light := it.Render(80, DefaultStyles(false))
	if dark == light {
		t.Errorf("expected different ANSI for dark vs light background styles")
	}
	for _, out := range []string{dark, light} {
		if !strings.Contains(plain(out), "bold") {
			t.Errorf("content lost across styles: %q", out)
		}
	}
}

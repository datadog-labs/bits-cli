package catalog

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func TestRenderGroupNonEmptyBothModes(t *testing.T) {
	for _, isDark := range []bool{true, false} {
		theme := styles.Default(isDark)
		for g := group(0); g < numGroups; g++ {
			body := renderGroup(g, 80, theme)
			if strings.TrimSpace(body) == "" {
				t.Fatalf("group %d (dark=%v) rendered empty", g, isDark)
			}
			if !strings.Contains(body, "end") {
				t.Fatalf("group %d missing end marker", g)
			}
		}
	}
}

func TestMarkdownGroupContainsHeadingText(t *testing.T) {
	// Strip ANSI first: glamour renders the H1 as a colored block, splitting the
	// text with escape sequences, so a raw substring check would miss it.
	body := ansi.Strip(renderGroup(groupMarkdown, 80, styles.Default(true)))
	if !strings.Contains(body, "H1 heading") {
		t.Fatalf("markdown group missing heading text: %q", body)
	}
}

func TestSharedComponentsGroupContainsProductionPanelAndSelector(t *testing.T) {
	body := ansi.Strip(renderGroup(groupSharedComponents, 100, styles.Default(true)))
	for _, want := range []string{
		"Text roles", "Muted metadata", "Feedback states", "Waiting for Datadog",
		"Authentication complete", "Login did not complete", "Text input", "Panel — full",
		"Panel — compact", "Panel — tiny", "Selector — selected", "Selector — narrow",
		"Choose your Datadog site", "US1", "app.datadoghq.com",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shared components page missing %q", want)
		}
	}
}

func TestColorTokensContainHex(t *testing.T) {
	body := renderGroup(groupColorTokens, 80, styles.Default(true))
	if !strings.Contains(body, "#") {
		t.Fatal("color tokens group missing hex values")
	}
}

// TestColorTokensIncludeMarkdownTokens guards that the glamour-side tokens
// (heading, link, inline code fg/bg) appear on the color page, not just the
// lipgloss ones — the page's purpose is to show every active token.
func TestColorTokensIncludeSharedComponentTokens(t *testing.T) {
	body := ansi.Strip(renderGroup(groupColorTokens, 120, styles.Default(true)))
	for _, label := range []string{"Text body", "Panel border", "Selector selected", "Feedback progress", "Text input cursor"} {
		if !strings.Contains(body, label) {
			t.Errorf("color tokens page missing %q", label)
		}
	}
}

func TestColorTokensIncludeMarkdownTokens(t *testing.T) {
	body := ansi.Strip(renderGroup(groupColorTokens, 120, styles.Default(true)))
	for _, label := range []string{"Markdown heading", "Markdown link", "Markdown inline code fg", "Markdown inline code bg"} {
		if !strings.Contains(body, label) {
			t.Errorf("color tokens page missing %q", label)
		}
	}
}

// TestRenderGroupWrapsToWidth is the scroll-model invariant: no rendered line
// may exceed the width, so one logical line is always one terminal row. Checked
// at a deliberately narrow width where samples would otherwise wrap.
func TestRenderGroupWrapsToWidth(t *testing.T) {
	const width = 24
	for _, isDark := range []bool{true, false} {
		theme := styles.Default(isDark)
		for g := group(0); g < numGroups; g++ {
			for i, line := range strings.Split(renderGroup(g, width, theme), "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("group %d (dark=%v) line %d width %d > %d: %q",
						g, isDark, i, w, width, ansi.Strip(line))
				}
			}
		}
	}
}

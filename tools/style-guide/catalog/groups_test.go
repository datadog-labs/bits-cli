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
			body := renderGroup(g, 80, theme, 0)
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
	body := ansi.Strip(renderGroup(groupMarkdown, 80, styles.Default(true), 0))
	if !strings.Contains(body, "H1 heading") {
		t.Fatalf("markdown group missing heading text: %q", body)
	}
}

func TestSharedComponentsGroupContainsProductionPanelAndSelector(t *testing.T) {
	body := ansi.Strip(renderGroup(groupSharedComponents, 100, styles.Default(true), 0))
	for _, want := range []string{
		"Text levels", "Secondary — supporting", "Feedback states", "Waiting for Datadog",
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
	body := renderGroup(groupColorTokens, 80, styles.Default(true), 0)
	if !strings.Contains(body, "#") {
		t.Fatal("color tokens group missing hex values")
	}
}

// TestColorTokensIncludeMarkdownTokens guards that the glamour-side tokens
// (heading, link, inline code fg/bg) appear on the color page, not just the
// lipgloss ones — the page's purpose is to show every active token.
func TestColorTokensIncludeSharedComponentTokens(t *testing.T) {
	body := ansi.Strip(renderGroup(groupColorTokens, 120, styles.Default(true), 0))
	for _, label := range []string{"Text primary", "Text secondary", "Text tertiary", "Panel border", "Selector selected", "Feedback progress", "Text input cursor"} {
		if !strings.Contains(body, label) {
			t.Errorf("color tokens page missing %q", label)
		}
	}
}

func TestColorTokensIncludeMarkdownTokens(t *testing.T) {
	body := ansi.Strip(renderGroup(groupColorTokens, 120, styles.Default(true), 0))
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
			for i, line := range strings.Split(renderGroup(g, width, theme, 0), "\n") {
				if w := ansi.StringWidth(line); w > width {
					t.Errorf("group %d (dark=%v) line %d width %d > %d: %q",
						g, isDark, i, w, width, ansi.Strip(line))
				}
			}
		}
	}
}

// TestTextAttributeSamplesCarryAnExplicitForeground guards the row documenting
// the terminal attributes. Those styles were bare lipgloss.NewStyle() with only
// an attribute set, so the samples rendered in whatever foreground the terminal
// supplied — on a background the guide paints itself. The theme-wide no-Faint
// walk in the styles package structurally cannot catch this: these are the
// guide's own styles, not the theme's.
func TestTextAttributeSamplesCarryAnExplicitForeground(t *testing.T) {
	for _, test := range []struct {
		name   string
		isDark bool
		want   string // truecolor SGR foreground for that mode's primary level
	}{
		{name: "dark", isDark: true, want: "38;2;255;255;255"},
		{name: "light", isDark: false, want: "38;2;0;0;0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			theme := styles.Default(test.isDark)
			samples := textAttrSamples(theme, theme.Text.Primary, "text attr")
			if len(samples) == 0 {
				t.Fatal("no attribute samples rendered")
			}
			for _, sample := range samples {
				// The title is rendered at primary as well, so asserting on the
				// whole string would pass on the title alone even with the
				// attribute style carrying no foreground at all. Split them.
				_, body, ok := strings.Cut(sample, "\n")
				if !ok {
					t.Fatalf("sample has no body beneath its title: %q", sample)
				}
				if !strings.Contains(body, test.want) {
					t.Errorf("attribute sample body renders without the primary foreground %s: %q",
						test.want, body)
				}
			}
		})
	}
}

// TestSamplesAreSeparatedByBlankRows covers the vertical rhythm: each rule is
// bracketed by two blank rows so a sample reads as its own block rather than
// as one more line of a continuous page. Checked against the rule line itself
// (a run of "─") rather than any blank line, because a sample's title and body
// are now also separated by a blank row and a looser check would pass even if
// the rule's own padding regressed.
func TestSamplesAreSeparatedByBlankRows(t *testing.T) {
	lines := strings.Split(ansi.Strip(renderGroup(groupTextAttrs, 100, styles.Default(true), 0)), "\n")
	isRule := func(s string) bool {
		s = strings.TrimSpace(s)
		return s != "" && strings.Trim(s, "─") == ""
	}
	var rules, flanked int
	for i, line := range lines {
		if !isRule(line) {
			continue
		}
		rules++
		above := i >= 2 && strings.TrimSpace(lines[i-1]) == "" && strings.TrimSpace(lines[i-2]) == ""
		below := i <= len(lines)-3 && strings.TrimSpace(lines[i+1]) == "" && strings.TrimSpace(lines[i+2]) == ""
		if above && below {
			flanked++
		}
	}
	if rules == 0 {
		t.Fatal("no rule lines found")
	}
	if flanked != rules {
		t.Errorf("expected every rule flanked by two blank rows, got %d/%d in:\n%s",
			flanked, rules, strings.Join(lines, "\n"))
	}
}

// TestSampleBodyIsSeparatedFromItsTitle covers the row of padding between a
// sample's title and what it names: a single blank row, distinct from the
// rule's own two, so a sample doesn't read as its title running straight into
// content.
func TestSampleBodyIsSeparatedFromItsTitle(t *testing.T) {
	theme := styles.Default(true)
	samples := textAttrSamples(theme, theme.Text.Primary, "text attr")
	if len(samples) == 0 {
		t.Fatal("no attribute samples rendered")
	}
	lines := strings.Split(samples[0], "\n")
	if len(lines) < 3 {
		t.Fatalf("expected title, blank row, body; got %d lines: %q", len(lines), samples[0])
	}
	if strings.TrimSpace(lines[1]) != "" {
		t.Errorf("expected a blank row between title and body, got %q", lines[1])
	}
	if strings.TrimSpace(ansi.Strip(lines[2])) == "" {
		t.Errorf("expected body content on the row after the blank, got %q", lines[2])
	}
}

// TestSampleTitlesCarryTheirPageTag covers the bracketed prefix naming what
// kind of page a sample lives on. Exercised through renderGroup, which is
// where the tag is actually chosen (groupTag) and threaded down — passing a
// tag directly into a leaf function would not catch a regression there.
func TestSampleTitlesCarryTheirPageTag(t *testing.T) {
	for _, test := range []struct {
		name   string
		group  group
		prefix string
	}{
		{"text attrs", groupTextAttrs, "[text attr] "},
		{"semantic roles", groupSemanticRoles, "[component] "},
		{"shared components", groupSharedComponents, "[component] "},
		{"markdown", groupMarkdown, "[markdown] "},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := ansi.Strip(renderGroup(test.group, 120, styles.Default(true), 0))
			firstLine := strings.SplitN(page, "\n", 2)[0]
			if !strings.HasPrefix(firstLine, test.prefix) {
				t.Errorf("expected title to start with %q, got %q", test.prefix, firstLine)
			}
		})
	}
}

// TestColorTokensAndMotionPagesCarryNoTag covers the two pages the prefix does
// not apply to: color tokens names each token directly, and the motion page's
// own [SELECTED]/[option] markers already say what a row is.
func TestColorTokensAndMotionPagesCarryNoTag(t *testing.T) {
	for _, g := range []group{groupColorTokens, groupStatusPillMotion} {
		page := ansi.Strip(renderGroup(g, 120, styles.Default(true), 0))
		for _, tag := range []string{"[text attr]", "[component]", "[markdown]"} {
			if strings.Contains(page, tag) {
				t.Errorf("group %d should carry no page tag, found %q in:\n%s", g, tag, page)
			}
		}
	}
}

// TestSampleTitlesRenderAtPrimary covers the wiring rather than the helper. A
// page's first line is its first sample's title, so it has to carry the primary
// level. Asserting this from inside textAttrSamples cannot catch a regression,
// because the title style is one of that function's arguments — only going
// through renderGroup exercises the choice renderGroup makes.
func TestSampleTitlesRenderAtPrimary(t *testing.T) {
	for _, test := range []struct {
		name   string
		isDark bool
		want   string // truecolor SGR foreground for that mode's primary level
	}{
		{name: "dark", isDark: true, want: "38;2;255;255;255"},
		{name: "light", isDark: false, want: "38;2;0;0;0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := renderGroup(groupTextAttrs, 100, styles.Default(test.isDark), 0)
			firstLine := strings.SplitN(page, "\n", 2)[0]
			if !strings.Contains(firstLine, test.want) {
				t.Errorf("first sample title does not render at primary %s: %q", test.want, firstLine)
			}
		})
	}
}

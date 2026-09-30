package catalog

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/components"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// sampleText is representative prose used to show text attributes and roles.
const sampleText = "The quick brown fox jumps over the lazy dog."

// renderGroup renders one page's body: its samples joined by delimiter lines,
// with an end-of-content marker so reaching the bottom is obvious. frame is the
// animation step; only pages whose animates() is true consult it.
func renderGroup(g group, width int, theme styles.Theme, frame int) string {
	sty := chat.StylesFor(theme)
	// The guide's own text splits by job. A sample's title names what is being
	// shown and has to be read, so it takes the primary level. Rules and the end
	// marker only divide the page, so they stay at tertiary and leave the
	// attention on the samples.
	title := theme.Text.Primary
	rule := theme.Text.Tertiary
	tag := groupTag(g)
	var samples []string
	switch g {
	case groupTextAttrs:
		samples = textAttrSamples(theme, title, tag)
	case groupSemanticRoles:
		samples = semanticRoleSamples(width, sty, title, tag)
	case groupSharedComponents:
		samples = sharedComponentSamples(width, theme, title, tag)
	case groupMarkdown:
		samples = markdownSamples(width, theme.IsDark, title, tag)
	case groupColorTokens:
		samples = colorTokenSamples(theme, title)
	case groupStatusPillMotion:
		samples = statusPillMotionSamples(width, sty, frame, title, tag)
	default:
		// numGroups is a count sentinel and is never rendered.
	}
	// Two blank rows around each rule so a sample reads as its own block instead
	// of as one more line of the page. Three "\n"s, not two: joining "a\n\nb"
	// collapses to a single blank line between them, so two blank rows need the
	// third newline that starts the second one.
	sep := "\n\n\n" + delimiter(width, rule) + "\n\n\n"
	body := strings.Join(samples, sep)
	end := rule.Render("— end —")
	// Hard-wrap to width so one logical ("\n") line renders as exactly one
	// terminal row. The scroll model counts and slices by "\n" (contentLines /
	// visibleLines); without this, long samples wrap in the terminal, the model
	// undercounts displayed rows, and the tail — including the footer — can scroll
	// off screen unreachably on narrow terminals.
	return wrapBody(body+sep+end, width)
}

// groupTag names the kind of page a group is, shown as a bracketed prefix on
// each of its sample titles. Semantic roles and shared components are both
// pages of individually styled UI pieces, so they share one tag; color tokens
// and the motion proposal are their own kind of page and get none.
func groupTag(g group) string {
	switch g {
	case groupTextAttrs:
		return "text attr"
	case groupSemanticRoles, groupSharedComponents:
		return "component"
	case groupMarkdown:
		return "markdown"
	default:
		return ""
	}
}

func textAttrSamples(theme styles.Theme, title lipgloss.Style, tag string) []string {
	// Each attribute builds on the primary level. These carried no foreground at
	// all before, so the row demonstrated its attributes in whatever color the
	// terminal happened to supply — on a background the app paints itself.
	base := theme.Text.Primary
	attrs := []struct {
		label string
		style lipgloss.Style
	}{
		{"Bold", base.Bold(true)},
		{"Italic", base.Italic(true)},
		// The only Faint left in the tree, and deliberately so: this page
		// documents what the terminal attributes look like, so Faint is the
		// subject here rather than a stand-in for a color. Everywhere else it was
		// dimming an unset foreground, which meant dimming the terminal's own.
		{"Faint", base.Faint(true)},
		{"Underline", base.Underline(true)},
		{"Reverse", base.Reverse(true)},
		{"Strikethrough", base.Strikethrough(true)},
	}
	out := make([]string, len(attrs))
	for i, a := range attrs {
		out[i] = renderSample(title, tag, a.label, a.style.Render(sampleText))
	}
	return out
}

func semanticRoleSamples(width int, sty chat.Styles, title lipgloss.Style, tag string) []string {
	return []string{
		renderSample(title, tag, "Input block", sty.Input.Block.Render(sty.Input.Marker.Render(sty.Input.Prompt)+sty.Input.Text.Render("show me error logs"))),
		renderSample(title, tag, "AssistantText", sty.AssistantText.Render(sampleText)),
		renderSample(title, tag, "Reasoning", sty.Reasoning.Render("thinking through the query plan…")),
		renderSample(title, tag, "ToolName", sty.ToolName.Render("search_logs")),
		renderSample(title, tag, "ToolDetail", sty.ToolDetail.Render("  ↳ {\"query\":\"timeout\"}")),
		renderSample(title, tag, "Meta (separator)", "a"+sty.Meta.Render(" · ")+"b"),
		// Status pills go through the real block renderer, so the catalog shows
		// exactly what a transcript shows (glyph + name + pill chip). This page is
		// a static reference, so the in-flight chip is pinned to its first frame;
		// its motion is shown on the Status pill motion page.
		renderSample(title, tag, "StatusRunning (via RenderBlock)", chat.RenderBlock(toolBlock(agent.ToolRunning), width, sty, 0)),
		renderSample(title, tag, "StatusSuccess (via RenderBlock)", chat.RenderBlock(toolBlock(agent.ToolSuccess), width, sty, 0)),
		renderSample(title, tag, "StatusError (via RenderBlock)", chat.RenderBlock(toolBlock(agent.ToolError), width, sty, 0)),
		renderSample(title, tag, "NoticeInfo", sty.NoticeInfo.Render(" heads up: restored 3 messages ")),
		renderSample(title, tag, "NoticeWarn", sty.NoticeWarn.Render(" warning: partial results ")),
		renderSample(title, tag, "NoticeError", sty.NoticeError.Render(" error: request failed ")),
	}
}

// toolBlock builds a representative tool block for the given status.
func toolBlock(status agent.ToolStatus) agent.Block {
	return agent.Block{
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{
			Name:   "search_logs",
			Input:  `{"query":"timeout"}`,
			Output: "ok: 4 results",
			Status: status,
		},
	}
}

func markdownSamples(width int, isDark bool, title lipgloss.Style, tag string) []string {
	docs := []struct{ label, src string }{
		{"Headings H1–H6", "# H1 heading\n## H2 heading\n### H3 heading\n#### H4 heading\n##### H5 heading\n###### H6 heading"},
		{"Inline formatting", "Prose with **bold**, *italic*, `inline code`, and a [link](https://docs.datadoghq.com)."},
		{"Bullet list", "- first item\n- second item\n- third item"},
		{"Ordered list", "1. first step\n2. second step\n3. third step"},
		{"Table", "| Service | P95 | Errors |\n| --- | --- | --- |\n| web | 42ms | 3 |\n| api | 88ms | 0 |"},
		{"Fenced code", "```go\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n```"},
		{"Blockquote", "> A quoted line of context.\n> A second quoted line."},
	}
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = renderSample(title, tag, d.label, chat.RenderMarkdown(d.src, width, isDark))
	}
	return out
}

func sharedComponentSamples(width int, theme styles.Theme, title lipgloss.Style, tag string) []string {
	panel := components.NewPanel(theme.Panel)
	panelContent := components.PanelContent{
		Title:   "Choose your Datadog site",
		Dismiss: "ESC x",
		Body: func(int, int) string {
			return theme.Text.Secondary.Render("Select the site where your organization lives.")
		},
		FooterLeft:     "↑/↓ navigate",
		FooterRight:    "enter to continue",
		CompactTitle:   "Sign in to Bits",
		CompactMessage: "Resize the terminal to continue.",
		TinyMessage:    "Resize to view panel",
	}

	selector := components.NewSelector([]components.Choice{
		{Label: "US1", Detail: "app.datadoghq.com"},
		{Label: "US3", Detail: "us3.datadoghq.com"},
		{Label: "Custom", Detail: "Enter another domain"},
	}, theme.Selector)
	selector.SetIndex(1)

	focused := textinput.New()
	focused.SetValue("acme.us3.datadoghq.com")
	focused.SetStyles(theme.TextInput)
	focused.Focus()
	blurred := textinput.New()
	blurred.Placeholder = "your-org.datadoghq.com"
	blurred.SetStyles(theme.TextInput)
	blurred.Blur()

	return []string{
		renderSample(title, tag, "Text levels", lipgloss.JoinVertical(lipgloss.Left,
			theme.Text.Primary.Render("Primary — what the reader came for"),
			theme.Text.Secondary.Render("Secondary — supporting, still meant to be read"),
			theme.Text.Tertiary.Render("Tertiary — present without competing"),
		)),
		renderSample(title, tag, "Feedback states", lipgloss.JoinVertical(lipgloss.Left,
			theme.Feedback.Progress.Render("⠋  Waiting for Datadog"),
			theme.Feedback.Success.Render("✓  Authentication complete"),
			theme.Feedback.Error.Render("Login did not complete"),
		)),
		renderSample(title, tag, "Text input — focused / blurred", focused.View()+"\n"+blurred.View()),
		renderSample(title, tag, "Panel — full", panel.View(width, 16, panelContent)),
		renderSample(title, tag, "Panel — compact", panel.View(min(width, 32), 6, panelContent)),
		renderSample(title, tag, "Panel — tiny", panel.View(min(width, 18), 2, panelContent)),
		renderSample(title, tag, "Selector — selected", selector.View(width)),
		renderSample(title, tag, "Selector — narrow", selector.View(min(width, 26))),
	}
}

func colorTokenSamples(theme styles.Theme, title lipgloss.Style) []string {
	sty := chat.StylesFor(theme)
	tokens := []struct {
		label string
		color color.Color
	}{
		{"Input surface", theme.Input.Block.GetBackground()},
		{"Input rule", theme.Input.Block.GetBorderTopForeground()},
		{"Editor menu item fg", theme.Editor.MenuItem.GetForeground()},
		{"Editor menu item bg", theme.Editor.MenuItem.GetBackground()},
		{"Editor menu selected fg", theme.Editor.MenuSelected.GetForeground()},
		{"Editor menu selected bg", theme.Editor.MenuSelected.GetBackground()},
		{"Interactive accent (UserMarker/ToolName fg)", sty.ToolName.GetForeground()},
		{"Tool detail fg", sty.ToolDetail.GetForeground()},
		{"Status running fg", sty.StatusRunning.GetForeground()},
		{"Status running bg", sty.StatusRunning.GetBackground()},
		{"Status sweep dim", sty.StatusSweepDim},
		{"Status sweep hot", sty.StatusSweepHot},
		// A succeeded tool renders as a bare glyph, so there is no success
		// surface to show here.
		{"Status success fg", sty.StatusSuccess.GetForeground()},
		{"Status error fg", sty.StatusError.GetForeground()},
		{"Status error bg", sty.StatusError.GetBackground()},
		{"Notice info bg", sty.NoticeInfo.GetBackground()},
		{"Notice warn bg", sty.NoticeWarn.GetBackground()},
		{"Notice error bg", sty.NoticeError.GetBackground()},
		{"Text primary fg", theme.Text.Primary.GetForeground()},
		{"Text secondary fg", theme.Text.Secondary.GetForeground()},
		{"Text tertiary fg", theme.Text.Tertiary.GetForeground()},
		{"Composer placeholder fg", theme.Input.Placeholder.GetForeground()},
		{"Panel border", theme.Panel.Frame.GetBorderTopForeground()},
		{"Selector selected fg", theme.Selector.Selected.GetForeground()},
		{"Feedback progress fg", theme.Feedback.Progress.GetForeground()},
		{"Feedback error fg", theme.Feedback.Error.GetForeground()},
		{"Feedback success fg", theme.Feedback.Success.GetForeground()},
		{"Text input cursor", theme.TextInput.Cursor.Color},
		// Glamour-side markdown tokens, exposed on chat.Styles so this page shows
		// every active color token, not just the lipgloss ones.
		{"Markdown heading (glamour secondary)", sty.MarkdownHeading},
		{"Markdown link (glamour)", sty.MarkdownLink},
		{"Markdown inline code fg (glamour)", sty.MarkdownCodeFg},
		{"Markdown inline code bg (glamour)", sty.MarkdownCodeBg},
	}
	var out []string
	for _, t := range tokens {
		if t.color == nil {
			continue
		}
		if _, ok := t.color.(lipgloss.NoColor); ok {
			continue
		}
		out = append(out, swatch(title, t.label, t.color))
	}
	return out
}

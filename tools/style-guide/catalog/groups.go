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
// with an end-of-content marker so reaching the bottom is obvious.
func renderGroup(g group, width int, theme styles.Theme) string {
	sty := chat.StylesFor(theme)
	var samples []string
	switch g {
	case groupTextAttrs:
		samples = textAttrSamples()
	case groupSemanticRoles:
		samples = semanticRoleSamples(width, sty)
	case groupSharedComponents:
		samples = sharedComponentSamples(width, theme)
	case groupMarkdown:
		samples = markdownSamples(width, theme.IsDark)
	case groupColorTokens:
		samples = colorTokenSamples(theme)
	default:
		// numGroups is a count sentinel and is never rendered.
	}
	sep := "\n" + delimiter(width) + "\n"
	body := strings.Join(samples, sep)
	end := lipgloss.NewStyle().Faint(true).Render("— end —")
	// Hard-wrap to width so one logical ("\n") line renders as exactly one
	// terminal row. The scroll model counts and slices by "\n" (contentLines /
	// visibleLines); without this, long samples wrap in the terminal, the model
	// undercounts displayed rows, and the tail — including the footer — can scroll
	// off screen unreachably on narrow terminals.
	return wrapBody(body+sep+end, width)
}

func textAttrSamples() []string {
	attrs := []struct {
		label string
		style lipgloss.Style
	}{
		{"Bold", lipgloss.NewStyle().Bold(true)},
		{"Italic", lipgloss.NewStyle().Italic(true)},
		{"Faint", lipgloss.NewStyle().Faint(true)},
		{"Underline", lipgloss.NewStyle().Underline(true)},
		{"Reverse", lipgloss.NewStyle().Reverse(true)},
		{"Strikethrough", lipgloss.NewStyle().Strikethrough(true)},
	}
	out := make([]string, len(attrs))
	for i, a := range attrs {
		out[i] = renderSample(a.label, a.style.Render(sampleText))
	}
	return out
}

func semanticRoleSamples(width int, sty chat.Styles) []string {
	return []string{
		renderSample("Input block", sty.Input.Block.Render(sty.Input.Marker.Render(sty.Input.Prompt)+sty.Input.Text.Render("show me error logs"))),
		renderSample("AssistantText", sty.AssistantText.Render(sampleText)),
		renderSample("Reasoning", sty.Reasoning.Render("thinking through the query plan…")),
		renderSample("ToolName", sty.ToolName.Render("search_logs")),
		renderSample("ToolDetail", sty.ToolDetail.Render("  ↳ {\"query\":\"timeout\"}")),
		renderSample("Meta (separator)", "a"+sty.Meta.Render(" · ")+"b"),
		// Status pills go through the real block renderer, so the catalog shows
		// exactly what a transcript shows (glyph + name + pill chip).
		renderSample("StatusRunning (via RenderBlock)", chat.RenderBlock(toolBlock(agent.ToolRunning), width, sty)),
		renderSample("StatusSuccess (via RenderBlock)", chat.RenderBlock(toolBlock(agent.ToolSuccess), width, sty)),
		renderSample("StatusError (via RenderBlock)", chat.RenderBlock(toolBlock(agent.ToolError), width, sty)),
		renderSample("NoticeInfo", sty.NoticeInfo.Render(" heads up: restored 3 messages ")),
		renderSample("NoticeWarn", sty.NoticeWarn.Render(" warning: partial results ")),
		renderSample("NoticeError", sty.NoticeError.Render(" error: request failed ")),
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

func markdownSamples(width int, isDark bool) []string {
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
		out[i] = renderSample(d.label, chat.RenderMarkdown(d.src, width, isDark))
	}
	return out
}

func sharedComponentSamples(width int, theme styles.Theme) []string {
	panel := components.NewPanel(theme.Panel)
	panelContent := components.PanelContent{
		Title:          "Choose your Datadog site",
		Dismiss:        "esc ×",
		Body:           func(int) string { return theme.Text.Muted.Render("Select the site where your organization lives.") },
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
		renderSample("Text roles", lipgloss.JoinVertical(lipgloss.Left,
			theme.Text.Body.Render("Body text"),
			theme.Text.Muted.Render("Muted metadata"),
			theme.Text.Help.Render("Keyboard help"),
		)),
		renderSample("Feedback states", lipgloss.JoinVertical(lipgloss.Left,
			theme.Feedback.Progress.Render("⠋  Waiting for Datadog"),
			theme.Feedback.Success.Render("✓  Authentication complete"),
			theme.Feedback.Error.Render("Login did not complete"),
		)),
		renderSample("Text input — focused / blurred", focused.View()+"\n"+blurred.View()),
		renderSample("Panel — full", panel.View(width, 16, panelContent)),
		renderSample("Panel — compact", panel.View(min(width, 32), 6, panelContent)),
		renderSample("Panel — tiny", panel.View(min(width, 18), 2, panelContent)),
		renderSample("Selector — selected", selector.View(width)),
		renderSample("Selector — narrow", selector.View(min(width, 26))),
	}
}

func colorTokenSamples(theme styles.Theme) []string {
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
		{"Status success fg", sty.StatusSuccess.GetForeground()},
		{"Status success bg", sty.StatusSuccess.GetBackground()},
		{"Status error fg", sty.StatusError.GetForeground()},
		{"Status error bg", sty.StatusError.GetBackground()},
		{"Notice info bg", sty.NoticeInfo.GetBackground()},
		{"Notice warn bg", sty.NoticeWarn.GetBackground()},
		{"Notice error bg", sty.NoticeError.GetBackground()},
		{"Text body fg", theme.Text.Body.GetForeground()},
		{"Text muted/help fg", theme.Text.Muted.GetForeground()},
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
		out = append(out, swatch(t.label, t.color))
	}
	return out
}

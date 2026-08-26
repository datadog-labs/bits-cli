package catalog

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

// sampleText is representative prose used to show text attributes and roles.
const sampleText = "The quick brown fox jumps over the lazy dog."

// renderGroup renders one page's body: its samples joined by delimiter lines,
// with an end-of-content marker so reaching the bottom is obvious.
func renderGroup(g group, width int, sty chat.Styles, isDark bool) string {
	var samples []string
	switch g {
	case groupTextAttrs:
		samples = textAttrSamples()
	case groupSemanticRoles:
		samples = semanticRoleSamples(width, sty)
	case groupMarkdown:
		samples = markdownSamples(width, isDark)
	case groupColorTokens:
		samples = colorTokenSamples(sty)
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
		Tool: &agent.ToolCall{
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

func colorTokenSamples(sty chat.Styles) []string {
	tokens := []struct {
		label string
		color color.Color
	}{
		{"AI primary (UserMarker/ToolName fg)", sty.ToolName.GetForeground()},
		{"Tool detail fg", sty.ToolDetail.GetForeground()},
		{"Status running fg", sty.StatusRunning.GetForeground()},
		{"Status success fg", sty.StatusSuccess.GetForeground()},
		{"Status success bg", sty.StatusSuccess.GetBackground()},
		{"Status error fg", sty.StatusError.GetForeground()},
		{"Status error bg", sty.StatusError.GetBackground()},
		{"Notice info bg", sty.NoticeInfo.GetBackground()},
		{"Notice warn bg", sty.NoticeWarn.GetBackground()},
		{"Notice error bg", sty.NoticeError.GetBackground()},
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

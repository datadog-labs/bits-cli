// Package catalog is a dev-only Bubble Tea page that renders shared text
// attributes, semantic roles, components, markdown elements, and color tokens
// at live terminal width and in either color mode. It has no engine
// or backend. Launch it via the style-guide tool: `go run ./tools/style-guide`.
package catalog

import "strings"

// group is one catalog page.
type group int

const (
	groupTextAttrs group = iota
	groupSemanticRoles
	groupSharedComponents
	groupMarkdown
	groupColorTokens
	numGroups // count sentinel; not a real page
)

func (g group) title() string {
	switch g {
	case groupTextAttrs:
		return "Text attributes"
	case groupSemanticRoles:
		return "Semantic roles (lipgloss)"
	case groupSharedComponents:
		return "Shared components"
	case groupMarkdown:
		return "Markdown elements (glamour)"
	case groupColorTokens:
		return "Color tokens"
	default:
		return "?"
	}
}

// nextGroup / prevGroup cycle through pages, wrapping at the ends.
func nextGroup(g group) group { return (g + 1) % numGroups }
func prevGroup(g group) group { return (g - 1 + numGroups) % numGroups }

// clampOffset keeps a scroll offset within [0, max(0, contentLines-viewLines)].
func clampOffset(offset, contentLines, viewLines int) int {
	maxOffset := contentLines - viewLines
	if maxOffset < 0 {
		maxOffset = 0
	}
	if offset < 0 {
		return 0
	}
	if offset > maxOffset {
		return maxOffset
	}
	return offset
}

// lineCount returns the number of lines in s (0 for empty).
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

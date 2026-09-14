// Package escape makes untrusted text safe for terminal rendering.
package escape

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const renderedTab = "    "

// Inline replaces control characters with visible representations so a value
// intended for one terminal row cannot emit terminal control sequences or add
// rows. C0 controls use their Unicode Control Picture; DEL and C1 controls use
// unambiguous visible forms.
func Inline(value string) string {
	if !hasControls(value, false, false) {
		return value
	}
	return replaceControls(value, false, false)
}

// Multiline makes untrusted multiline plain text ready for terminal layout.
// CRLF is normalized to LF, each tab is rendered as four spaces, and every
// other terminal control is replaced with a visible representation. The tab
// replacement is deliberately a fixed display policy rather than terminal
// tab-stop emulation; callers must retain the original value as source data.
func Multiline(value string) string {
	return replaceControls(normalizeLineEndings(value), true, false)
}

// MarkdownSource makes untrusted Markdown safe to pass to a parser. Unlike
// Multiline, it preserves tabs so the parser can apply Markdown's
// column-sensitive rules. The styled result must pass through StyledMultiline
// before it reaches the terminal.
func MarkdownSource(value string) string {
	return replaceControls(normalizeLineEndings(value), true, true)
}

// RenderTabs replaces tabs with a stable four-space terminal representation.
// It is intended for rendered output, after any syntax-sensitive parsing.
func RenderTabs(value string) string {
	return strings.ReplaceAll(value, "\t", renderedTab)
}

// StyledMultiline makes output from a trusted text renderer safe for the
// terminal. It preserves the SGR styling and OSC 8 hyperlinks produced by the
// renderer, while making every other control visible and rendering tabs with
// the same fixed policy as Multiline. Untrusted text must be sanitized before
// it is handed to the renderer.
func StyledMultiline(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for i := 0; i < len(value); {
		if value[i] == '\x1b' {
			if n := supportedEscapeLen(value[i:]); n > 0 {
				b.WriteString(value[i : i+n])
				i += n
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(value[i:])
		switch {
		case r == '\r' && i+size < len(value) && value[i+size] == '\n':
			b.WriteByte('\n')
			i += size + 1
			continue
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(renderedTab)
		case r <= 0x1f:
			b.WriteRune('\u2400' + r)
		case r == 0x7f:
			b.WriteRune('\u2421')
		case r >= 0x80 && r <= 0x9f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// SingleLine removes terminal styling and format characters, replaces controls
// with spaces, and collapses whitespace. It is for compact labels and metadata
// where preserving line boundaries or displaying control pictures is not useful.
func SingleLine(value string) string {
	if !needsSingleLine(value) {
		return value
	}
	value = ansi.Strip(value)
	value = strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cf) {
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func hasControls(value string, preserveNewlines, preserveTabs bool) bool {
	for _, r := range value {
		if preserveNewlines && r == '\n' {
			continue
		}
		if preserveTabs && r == '\t' {
			continue
		}
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

func needsSingleLine(value string) bool {
	if value == "" {
		return false
	}
	previousSpace := true
	for _, r := range value {
		if unicode.In(r, unicode.Cf) || unicode.IsControl(r) {
			return true
		}
		if unicode.IsSpace(r) {
			if r != ' ' || previousSpace {
				return true
			}
			previousSpace = true
			continue
		}
		previousSpace = false
	}
	return previousSpace
}

func normalizeLineEndings(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

func supportedEscapeLen(value string) int {
	if len(value) < 3 || value[0] != '\x1b' {
		return 0
	}
	switch value[1] {
	case '[':
		i := 2
		for i < len(value) && value[i] >= 0x30 && value[i] <= 0x3f {
			i++
		}
		for i < len(value) && value[i] >= 0x20 && value[i] <= 0x2f {
			i++
		}
		if i < len(value) && value[i] == 'm' {
			return i + 1
		}
	case ']':
		if !strings.HasPrefix(value, "\x1b]8;") {
			return 0
		}
		for i := len("\x1b]8;"); i < len(value); i++ {
			if value[i] == '\x07' || (value[i] == '\x1b' && i+1 < len(value) && value[i+1] == '\\') {
				if !hasControls(value[len("\x1b]8;"):i], false, false) {
					if value[i] == '\x07' {
						return i + 1
					}
					return i + 2
				}
				return 0
			}
		}
	}
	return 0
}

func replaceControls(value string, preserveNewlines, preserveTabs bool) string {
	if !hasControls(value, preserveNewlines, preserveTabs) {
		return value
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case preserveNewlines && r == '\n':
			b.WriteRune(r)
		case preserveTabs && r == '\t':
			b.WriteRune(r)
		case preserveNewlines && r == '\t':
			b.WriteString(renderedTab)
		case r <= 0x1f:
			b.WriteRune('\u2400' + r)
		case r == 0x7f:
			b.WriteRune('\u2421')
		case r >= 0x80 && r <= 0x9f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

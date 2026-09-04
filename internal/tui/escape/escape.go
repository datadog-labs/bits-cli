// Package escape makes untrusted text safe for terminal rendering.
package escape

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Inline replaces control characters with visible representations so a value
// intended for one terminal row cannot emit terminal control sequences or add
// rows. C0 controls use their Unicode Control Picture; DEL and C1 controls use
// unambiguous visible forms.
func Inline(value string) string {
	if !hasControls(value, false) {
		return value
	}
	return replaceControls(value, false)
}

// Multiline replaces terminal control characters while preserving line feeds.
// Use it for untrusted text whose newlines are part of the intended layout,
// such as Markdown, tool output, and progress updates.
func Multiline(value string) string {
	if !hasControls(value, true) {
		return value
	}
	return replaceControls(value, true)
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

func hasControls(value string, preserveNewlines bool) bool {
	for _, r := range value {
		if preserveNewlines && r == '\n' {
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

func replaceControls(value string, preserveNewlines bool) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case preserveNewlines && r == '\n':
			b.WriteRune(r)
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

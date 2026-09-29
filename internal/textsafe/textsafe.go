// Package textsafe holds predicates shared by packages that neutralize
// untrusted text before it reaches a terminal or an error string.
package textsafe

// IsBidiControl reports whether r is a bidi-format control that can visually
// reorder text. ZWJ (U+200D) and ZWNJ (U+200C) are not bidi controls and are
// preserved by callers that need them.
func IsBidiControl(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E,
		r >= 0x2066 && r <= 0x2069,
		r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	}
	return false
}

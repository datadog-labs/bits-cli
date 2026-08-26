package chat

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func hex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02X%02X%02X", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// TestDefaultStylesMarkdownTokens verifies the exposed markdown token colors
// track the mode-specific constants datadogStyleConfig feeds to glamour, so the
// two representations can't silently drift.
func TestDefaultStylesMarkdownTokens(t *testing.T) {
	cases := []struct {
		isDark bool
	}{
		{true},
		{false},
	}
	for _, tc := range cases {
		sty := DefaultStyles(tc.isDark)
		want := styles.Default(tc.isDark).Chat
		got := []struct{ name, have, want string }{
			{"heading", hex(sty.MarkdownHeading), hex(want.MarkdownHeading)},
			{"link", hex(sty.MarkdownLink), hex(want.MarkdownLink)},
			{"code fg", hex(sty.MarkdownCodeFg), hex(want.MarkdownCodeFg)},
			{"code bg", hex(sty.MarkdownCodeBg), hex(want.MarkdownCodeBg)},
		}
		for _, g := range got {
			if !equalHex(g.have, g.want) {
				t.Errorf("dark=%v %s = %s, want %s", tc.isDark, g.name, g.have, g.want)
			}
		}
	}
}

// equalHex compares two #RRGGBB strings case-insensitively.
func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if 'a' <= ca && ca <= 'f' {
			ca -= 'a' - 'A'
		}
		if 'a' <= cb && cb <= 'f' {
			cb -= 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

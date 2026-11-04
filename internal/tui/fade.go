package tui

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// fade returns content with every cell's colors mixed three quarters of the
// way toward bg. A nil foreground is the terminal default, so it fades from
// text; a nil background already paints as bg and stays nil.
func fade(content string, width, height int, text, bg color.Color) string {
	buf := uv.NewScreenBuffer(width, height)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(content).Draw(&buf, buf.Bounds())

	for _, line := range buf.Lines {
		for x := range line {
			style := &line[x].Style
			if style.Fg == nil {
				style.Fg = text
			}
			style.Fg = fadeColor(style.Fg, bg)
			if style.Bg != nil {
				style.Bg = fadeColor(style.Bg, bg)
			}
		}
	}
	return buf.Render()
}

// fadeColor mixes c three quarters of the way toward bg per 8-bit channel,
// rounding half up.
func fadeColor(c, bg color.Color) color.Color {
	cr, cg, cb, _ := c.RGBA()
	bgR, bgG, bgB, _ := bg.RGBA()
	mix := func(a, b uint32) uint8 { return uint8((a>>8 + 3*(b>>8) + 2) / 4) }
	return color.RGBA{R: mix(cr, bgR), G: mix(cg, bgG), B: mix(cb, bgB), A: 0xFF}
}

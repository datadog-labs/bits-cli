package tui

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// fade returns content with every cell's colors mixed halfway toward bg. A
// nil foreground is the terminal default, so it fades from text; a nil
// background already paints as bg and stays nil.
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
			style.Fg = blendHalf(style.Fg, bg)
			if style.Bg != nil {
				style.Bg = blendHalf(style.Bg, bg)
			}
		}
	}
	return buf.Render()
}

// blendHalf mixes c halfway toward bg per 8-bit channel, rounding half up.
func blendHalf(c, bg color.Color) color.Color {
	cr, cg, cb, _ := c.RGBA()
	br, bgr, bb, _ := bg.RGBA()
	mix := func(a, b uint32) uint8 { return uint8((a>>8 + b>>8 + 1) / 2) }
	return color.RGBA{R: mix(cr, br), G: mix(cg, bgr), B: mix(cb, bb), A: 0xFF}
}

package components

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// PaintRowBackground sets the background of a rendered line to bg, across the
// full width. Text colors and attributes are kept.
func PaintRowBackground(line string, width int, bg color.Color) string {
	return repaintRow(line, width, func(color.Color) color.Color { return bg })
}

// ShadeRow highlights a rendered line, such as on hover. Each background moves
// by the same amount canvas moves to become shade, so colored cells like diff
// rows keep their color. Cells without a background become shade.
func ShadeRow(line string, width int, canvas, shade color.Color) string {
	if canvas == nil || shade == nil {
		return PaintRowBackground(line, width, shade)
	}
	return repaintRow(line, width, func(bg color.Color) color.Color {
		if bg == nil {
			return shade
		}
		return offset(bg, canvas, shade)
	})
}

func repaintRow(line string, width int, paint func(color.Color) color.Color) string {
	if width <= 0 {
		return line
	}
	buf := uv.NewScreenBuffer(width, 1)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(line).Draw(&buf, buf.Bounds())
	row := buf.Lines[0]
	for i := range row {
		row[i].Style.Bg = paint(row[i].Style.Bg)
	}
	return buf.Render()
}

// offset moves c by the difference between from and to.
func offset(c, from, to color.Color) color.Color {
	cr, cg, cb, _ := c.RGBA()
	fr, fg, fb, _ := from.RGBA()
	tr, tg, tb, _ := to.RGBA()
	shift := func(c, from, to uint32) uint8 {
		return uint8(min(max(int(c>>8)+int(to>>8)-int(from>>8), 0), 0xff))
	}
	return color.RGBA{R: shift(cr, fr, tr), G: shift(cg, fg, tg), B: shift(cb, fb, tb), A: 0xff}
}

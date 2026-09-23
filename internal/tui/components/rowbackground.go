package components

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// PaintRowBackground repaints every cell of an already-rendered single line to
// bg, preserving foreground/underline/attrs. Cells beyond the line's own
// content, up to width, are blank and get bg too, so the fill reaches past a
// line shorter than width.
func PaintRowBackground(line string, width int, bg color.Color) string {
	if width <= 0 {
		return line
	}
	buf := uv.NewScreenBuffer(width, 1)
	buf.Method = ansi.GraphemeWidth
	uv.NewStyledString(line).Draw(&buf, buf.Bounds())
	row := buf.Lines[0]
	for i := range row {
		row[i].Style.Bg = bg
	}
	return buf.Render()
}

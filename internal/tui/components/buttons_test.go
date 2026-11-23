package components

import (
	"image"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestJoinButtonsReportsEachButtonsCells(t *testing.T) {
	row, rects := JoinButtons([]string{" Allow ", "\x1b[1m Deny \x1b[m"})
	if got := ansi.Strip(row); got != " Allow    Deny " {
		t.Fatalf("row = %q", got)
	}
	want := []image.Rectangle{image.Rect(0, 0, 7, 1), image.Rect(9, 0, 15, 1)}
	for n := range want {
		if rects[n] != want[n] {
			t.Errorf("button %d covers %v, want %v", n, rects[n], want[n])
		}
	}
}

// placedRow shows buttons 0, 2, and 3 of four, as a row that dropped
// button 1, at x 0-5, 7-12, and 14-19 of row 2.
func placedRow(selected int, focused bool) *ButtonRow {
	row := &ButtonRow{Selected: selected}
	row.Place([]PlacedButton{
		{Index: 0, At: image.Rect(0, 2, 5, 3)},
		{Index: 2, At: image.Rect(7, 2, 12, 3)},
		{Index: 3, At: image.Rect(14, 2, 19, 3)},
	}, focused)
	return row
}

func TestButtonRow(t *testing.T) {
	key := func(code rune, mod tea.KeyMod) tea.Msg { return tea.KeyPressMsg{Code: code, Mod: mod} }
	click := func(x int, button tea.MouseButton) tea.Msg {
		return tea.MouseClickMsg(tea.Mouse{X: x, Y: 2, Button: button})
	}
	for _, tt := range []struct {
		name     string
		focused  bool
		selected int
		msg      tea.Msg
		want     int
		pressed  bool
		unused   bool
	}{
		{name: "right skips what was not placed", focused: true, msg: key(tea.KeyRight, 0), want: 2},
		{name: "right wraps", focused: true, selected: 3, msg: key(tea.KeyRight, 0), want: 0},
		{name: "left wraps", focused: true, msg: key(tea.KeyLeft, 0), want: 3},
		{name: "tab", focused: true, selected: 2, msg: key(tea.KeyTab, 0), want: 3},
		{name: "enter presses", focused: true, selected: 2, msg: key(tea.KeyEnter, 0), want: 2, pressed: true},
		{name: "unfocused tab enters first", selected: 3, msg: key(tea.KeyTab, 0), want: 0},
		{name: "unfocused shift+tab enters last", msg: key(tea.KeyTab, tea.ModShift), want: 3},
		{name: "unfocused leaves arrows", msg: key(tea.KeyRight, 0), unused: true},
		{name: "unfocused leaves enter", msg: key(tea.KeyEnter, 0), unused: true},
		{name: "click selects", focused: true, msg: click(8, tea.MouseLeft), want: 2},
		{name: "click on the selected presses", focused: true, msg: click(1, tea.MouseLeft), want: 0, pressed: true},
		{name: "unfocused click only selects", msg: click(1, tea.MouseLeft), want: 0},
		{name: "the gap is no button", focused: true, msg: click(6, tea.MouseLeft), unused: true},
		{name: "right click", focused: true, msg: click(8, tea.MouseRight), unused: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			row := placedRow(tt.selected, tt.focused)
			pressed, used := row.Update(tt.msg)
			if row.Selected != tt.want || pressed != tt.pressed || used == tt.unused {
				t.Fatalf("selected %d, pressed %v, used %v; want %d, %v, %v", row.Selected, pressed, used, tt.want, tt.pressed, !tt.unused)
			}
		})
	}
}

package components

import (
	"image"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// buttonGap separates the buttons of a row.
const buttonGap = "  "

// JoinButtons draws rendered button labels as a row and reports the cells
// each one covers, relative to the row's start.
func JoinButtons(labels []string) (string, []image.Rectangle) {
	rects := make([]image.Rectangle, len(labels))
	x := 0
	for n, label := range labels {
		width := ansi.StringWidth(label)
		rects[n] = image.Rect(x, 0, x+width, 1)
		x += width + ansi.StringWidth(buttonGap)
	}
	return strings.Join(labels, buttonGap), rects
}

// PlacedButton is a button its owner drew: its index among the owner's
// buttons and the cells it covers, empty when the form shown has no button
// to click.
type PlacedButton struct {
	Index int
	At    image.Rectangle
}

// ButtonRow is the selection of a row of buttons its owner draws. Selected
// moves among the buttons the owner placed, so the user can never select one
// they cannot see.
//
// A click selects the button under it; a click on the selected button of a
// focused row presses it, as enter does. A row that is not focused leaves
// left, right, and enter to the surface that holds the keyboard.
type ButtonRow struct {
	Selected int // the owner's index of the selected button

	focused bool           // whether the row held the keyboard as last placed
	placed  []PlacedButton // in order
}

// Place records the buttons as the owner drew them.
func (r *ButtonRow) Place(buttons []PlacedButton, focused bool) {
	r.placed, r.focused = buttons, focused
}

// Update applies a key or a click and reports whether it pressed the
// selected button and whether the row used it.
func (r *ButtonRow) Update(msg tea.Msg) (pressed, used bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "left":
			if !r.focused {
				return false, false
			}
			r.cycle(-1)
		case "right":
			if !r.focused {
				return false, false
			}
			r.cycle(1)
		case "shift+tab":
			r.cycle(-1)
		case "tab":
			r.cycle(1)
		case "enter":
			return r.focused, r.focused
		default:
			return false, false
		}
		return false, true
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return false, false
		}
		for _, b := range r.placed {
			if image.Pt(msg.X, msg.Y).In(b.At) {
				pressed = r.focused && b.Index == r.Selected
				r.Selected = b.Index
				return pressed, true
			}
		}
	}
	return false, false
}

// cycle moves the selection by delta among the placed buttons. A row that is
// not focused gets the keyboard on the first button going forward, the last
// going back.
func (r *ButtonRow) cycle(delta int) {
	if len(r.placed) == 0 {
		return
	}
	pos := slices.IndexFunc(r.placed, func(b PlacedButton) bool { return b.Index == r.Selected })
	switch {
	case !r.focused && delta > 0:
		pos = 0
	case !r.focused:
		pos = len(r.placed) - 1
	default:
		pos = (max(0, pos) + delta + len(r.placed)) % len(r.placed)
	}
	r.Selected = r.placed[pos].Index
}

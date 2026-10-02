package components

import (
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Slot is what a host tells a prompt each time it lays it out: the area it
// may take, and how many requests wait on the user.
type Slot struct {
	Width, Height int
	Waiting       int // requests waiting on the user, this one included
}

// Placement is where a prompt asks its host to show it. Both placements take
// rows from the host's layout; a floating one could join them, drawn over the
// rest at a size and anchor of its own.
type Placement uint8

const (
	// Docked shows the prompt next to the host's input, which stays visible
	// but inert while the prompt owns the keyboard.
	Docked Placement = iota
	// ReplacesInput shows the prompt in place of the host's input, which
	// hides, with the rest of the screen dimmed behind it.
	ReplacesInput
)

// Prompt is the interactive surface of one request to the user, such as a
// tool approval or a questionnaire a tool asks through. Its host gives it an
// area and routes it input. Mouse coordinates are relative to the prompt's
// top-left corner.
//
// Prompt is the UI only: how the answer is read and where it goes is up to
// whoever owns the request, since only they know the answer's type.
type Prompt interface {
	// Update applies a key or a pointer event. It reports whether the prompt
	// used the event, so the host can give an ignored click to text selection.
	Update(tea.Msg) (tea.Cmd, bool)
	// Layout fits the prompt to the slot's width and at most its height and
	// renders it. It reports whether the prompt can be answered as drawn: every
	// control shows, and so does the request. A host must not route input to a
	// prompt it drew unanswerable, so a hidden choice is never made.
	//
	// Layout is also where a prompt learns its size and settles its scroll, so
	// a host lays a prompt out once per update and draws what Layout returned.
	Layout(slot Slot) (view string, answerable bool)
	SetStyles(theme styles.Theme)
	Placement() Placement
}

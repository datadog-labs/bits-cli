package components

import (
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Dock is what a host tells a prompt each time it lays it out: the area it
// may take, and how many requests wait on the user.
type Dock struct {
	Width, Height int
	Waiting       int // requests waiting on the user, this one included
}

// Prompt is an interactive surface that answers one request from the user,
// such as a tool approval or a questionnaire a tool asks through. Its host
// gives it an area, routes it input, and collects its answer after each
// update; once it has the answer it routes the prompt no more input. Mouse
// coordinates are relative to the prompt's top-left corner.
//
// A component such as Questionnaire is wrapped by whoever owns the request,
// since only they know the answer's type.
type Prompt interface {
	// Update applies a key or a pointer event. It reports whether the prompt
	// used the event, so the host can give an ignored click to text selection.
	Update(tea.Msg) (tea.Cmd, bool)
	// Layout fits the prompt to the dock's width and at most its height and
	// renders it. It reports whether the prompt can be answered as drawn: every
	// control shows, and so does the request. A host must not route input to a
	// prompt it drew unanswerable, so a hidden choice is never made.
	//
	// Layout is also where a prompt learns its size and settles its scroll, so
	// a host lays a prompt out once per update and draws what Layout returned.
	Layout(dock Dock) (view string, answerable bool)
	SetStyles(theme styles.Theme)
	// Result reports the user's answer once they have given it, and keeps
	// reporting it. Whoever asked, not the prompt, turns it into the request's
	// result.
	Result() (any, bool)
}

package components

import (
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Prompt is an interactive surface that answers one request from the user,
// such as a tool approval or a questionnaire a tool asks through. Its host
// gives it an area, routes it input, and collects its answer after each
// update. Mouse coordinates are relative to the prompt's top-left corner.
//
// A component such as Questionnaire is wrapped by whoever owns the request,
// since only they know the answer's type.
type Prompt interface {
	// Update applies a key or a pointer event. It reports whether the prompt
	// used the event, so the host can give an ignored click to text selection.
	Update(tea.Msg) (tea.Cmd, bool)
	// Layout fits the prompt to width and at most height rows and renders it.
	Layout(width, height int) string
	// MinSize is the smallest area the prompt can be answered in. A host must
	// not route input to a prompt it cannot show at that size.
	MinSize() (width, height int)
	SetStyles(theme styles.Theme)
	// Result hands over the user's answer once they have given it. Whoever
	// asked, not the prompt, turns it into the request's result.
	Result() (any, bool)
}

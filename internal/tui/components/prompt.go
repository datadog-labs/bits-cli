package components

import (
	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/tui/styles"
)

// Slot gives a prompt its available size and queue length.
type Slot struct {
	Width, Height int
	Waiting       int // requests waiting on the user, this one included
}

// Placement tells the host where to show a prompt.
type Placement uint8

const (
	// Docked shows the prompt next to the host's input, which stays visible
	// but inert while the prompt owns the keyboard.
	Docked Placement = iota
	// ReplacesInput shows the prompt in place of the host's input, which
	// hides, with the rest of the screen dimmed behind it.
	ReplacesInput
)

// Prompt is an interactive request UI. The host sizes it and routes input;
// the request owner handles its answer.
type Prompt interface {
	// Update applies input and reports whether the prompt used it.
	Update(tea.Msg) (tea.Cmd, bool)
	// Layout renders within the slot and reports whether the full prompt is answerable.
	// Hosts should lay it out once per update and draw the returned view.
	Layout(slot Slot) (view string, answerable bool)
	SetStyles(theme styles.Theme)
	Placement() Placement
}

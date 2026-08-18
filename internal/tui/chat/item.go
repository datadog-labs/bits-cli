// Package chat holds the in-memory transcript store for the TUI: the ordered
// list of rendered conversation items plus the phase/permission UI enums. It is
// UI-domain state, patched in place from agent events by the tui layer. It does
// not import Bubble Tea and is not safe for concurrent use (the tui owns it on
// the render thread).
package chat

import "github.com/DataDog/bits-cli/internal/assistant"

// PermissionMode is the local trust dial for tool execution. Surfaced in the
// status line for now; enforcement lands with client tools later.
type PermissionMode int

const (
	PermDefault PermissionMode = iota // ask every time
	PermAcceptEdits
	PermPlan
	PermYolo
)

// Phase is the turn state used by the status line and esc/cancel handling.
type Phase int

const (
	PhaseIdle      Phase = iota
	PhaseWaiting         // sent, awaiting first token
	PhaseStreaming       // tokens arriving
	PhaseError
)

// ToolStatus is the lifecycle state of a tool block. The zero value ToolUnknown
// means "unset", which lets a merge leave an existing status untouched.
type ToolStatus int

const (
	ToolUnknown ToolStatus = iota
	ToolRunning
	ToolSuccess
	ToolError
)

// ToolStatusOf maps a wire tool_response status ("success" / "error", or
// "running") to a ToolStatus. Empty or unrecognized values map to ToolUnknown.
func ToolStatusOf(s string) ToolStatus {
	switch s {
	case "running":
		return ToolRunning
	case "success":
		return ToolSuccess
	case "error":
		return ToolError
	default:
		return ToolUnknown
	}
}

// ToolView is the rendered state of a tool call+result block. A call creates it
// (Status ToolRunning) and the matching result merges into it.
type ToolView struct {
	Name   string
	Input  string
	Output string
	Status ToolStatus
}

// Item is one rendered block in the transcript. Items are keyed by ID for O(1)
// patching; Version is bumped on every mutation and used as a render-memo key
// (with ID + width) so only changed items re-render.
type Item struct {
	ID        string
	Role      assistant.Role
	Kind      assistant.ContentKind
	Text      string
	Tool      ToolView
	Streaming bool
	Collapsed bool // reasoning / long tool output (Ctrl+O later)
	Version   int
}

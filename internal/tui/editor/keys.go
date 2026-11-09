package editor

// inputFocus identifies the editor control receiving keys. The root still owns
// focus between the editor and other screens; these controls share that owner.
type inputFocus uint8

const (
	focusText inputFocus = iota
	focusAttachments
	focusQueue
	focusCount
)

// ControlKey gives editor controls precedence over the parent's Enter/Escape
// actions. Completion routing runs first in the parent. Unhandled keys return
// to the text, where normal editing and history bindings remain unchanged.
func (e *Editor) ControlKey(key string) bool {
	if !e.Focused() || e.MenuOpen() {
		return false
	}
	if key == "tab" || key == "shift+tab" {
		direction := 1
		if key == "shift+tab" {
			direction = -1
		}
		for range focusCount {
			e.focus = inputFocus((int(e.focus) + direction + int(focusCount)) % int(focusCount))
			if e.focusAvailable(e.focus) {
				break
			}
		}
		if e.focus == focusAttachments {
			e.focusChip(min(e.chips.cursor, len(e.attachments)-1))
		}
		e.invalidateBody()
		return true
	}
	if e.focus == focusQueue {
		switch key {
		case "up", "ctrl+p":
			e.queueUI.cursor = max(0, e.queueUI.cursor-1)
		case "down", "ctrl+n":
			e.queueUI.cursor = min(e.QueueLen()-1, e.queueUI.cursor+1)
		case "backspace", "delete":
			e.RemoveQueued(e.queueUI.cursor)
		case "esc", "enter":
			e.focus = focusText
		default:
			e.focus = focusText
			e.invalidateBody()
			return false
		}
		e.invalidateBody()
		return true
	}
	return e.chipKey(key)
}

func (e *Editor) focusAvailable(focus inputFocus) bool {
	switch focus {
	case focusText:
		return true
	case focusAttachments:
		return len(e.attachments) > 0
	case focusQueue:
		return len(e.queueUI.hits) > 0
	default:
		return false
	}
}

package styles

import "fmt"

// Mode selects how the terminal background drives theme polarity. Bits normally
// reads the background over OSC 11, which multiplexers swallow and which cannot
// be faked from outside the terminal; pinning a mode makes the other polarity
// reachable for review and screenshots.
type Mode string

const (
	// ModeAuto takes the polarity the terminal reports.
	ModeAuto Mode = "auto"
	// ModeLight forces the light theme whatever the terminal reports.
	ModeLight Mode = "light"
	// ModeDark forces the dark theme whatever the terminal reports.
	ModeDark Mode = "dark"
)

// Dark reports the polarity to render at, given the background the terminal
// detected. The zero Mode behaves as ModeAuto so an unset config still follows
// the terminal.
func (m Mode) Dark(detectedDark bool) bool {
	switch m {
	case ModeLight:
		return false
	case ModeDark:
		return true
	default:
		return detectedDark
	}
}

// ParseMode maps a flag value to a Mode, rejecting anything else.
func ParseMode(raw string) (Mode, error) {
	switch mode := Mode(raw); mode {
	case ModeAuto, ModeLight, ModeDark:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid theme %q; expected auto, light, or dark", raw)
	}
}

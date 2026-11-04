// Package graphics draws raster images in the transcript through the Kitty
// graphics protocol, and tracks whether the terminal can paint them.
//
// Image bytes cannot travel in a Bubble Tea view — ultraviolet's printString
// parses content into cells and drops unrecognized escape sequences. Hence
// virtual placement: pixels transmit out of band through tea.Raw, and the view
// holds only printable placeholder cells marking where to paint them.
package graphics

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// ProbeID is distinct from every image ID so a probe reply cannot be mistaken
// for an image's.
const ProbeID = 31

// Support is what the terminal is known to paint. It starts Unknown, becomes
// Supported when the probe answers, and Unsupported when the probe fails or
// the canary image is rejected. Unsupported is final.
type Support uint8

const (
	Unknown Support = iota
	Supported
	Unsupported
)

// Query probes for placeholder support, or returns nil on a terminal that must
// not be asked. A device attributes request follows in the same write: every
// terminal answers it, and in order, so its reply settles a probe that was
// silently ignored. Nothing needs to time out.
func Query() tea.Cmd {
	if !paintsPlaceholders() {
		return nil
	}
	return tea.Raw(ansi.KittyGraphics(
		[]byte("AAAA"),
		"i="+strconv.Itoa(ProbeID), "s=1", "v=1", "a=q", "t=d", "f=24",
	) + ansi.RequestPrimaryDeviceAttributes)
}

// Observe applies a terminal reply and reports whether it confirmed support.
// Only the probe and the canary, an image every session stores, speak for the
// terminal: another image's error is that image's own problem.
func (s *Support) Observe(event uv.KittyGraphicsEvent, canary int) bool {
	ok := string(event.Payload) == "OK"
	switch id := event.Options.ID; {
	case !ok && (id == ProbeID || id == canary):
		*s = Unsupported
	case ok && id == ProbeID && *s == Unknown:
		*s = Supported
		return true
	}
	return false
}

// ObserveDeviceAttributes settles a probe the terminal ignored: it would have
// answered before the device attributes it was asked for next.
func (s *Support) ObserveDeviceAttributes() {
	if *s == Unknown {
		*s = Unsupported
	}
}

// ObserveColorProfile rules out a terminal not driven in truecolor:
// placeholders find their image through an exact 24-bit foreground color,
// which a downsampled profile would lose or change.
func (s *Support) ObserveColorProfile(p colorprofile.Profile) {
	if p < colorprofile.TrueColor {
		*s = Unsupported
	}
}

// paintsPlaceholders reports whether the terminal is known to paint Unicode
// placeholders, the one capability in the protocol with no query of its own.
// Answering the graphics query does not imply it: iTerm2 answers, then ignores
// the U key and draws the placeholder rune as an unknown glyph. The reply
// proves only that the protocol is reachable.
func paintsPlaceholders() bool {
	term := os.Getenv("TERM")
	// A multiplexer does not forward placements, yet the outer terminal may
	// still answer the query.
	if os.Getenv("TMUX") != "" || os.Getenv("STY") != "" ||
		strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux") {
		return false
	}
	switch {
	case strings.Contains(term, "kitty"):
		return true
	case term == "xterm-ghostty", strings.EqualFold(os.Getenv("TERM_PROGRAM"), "ghostty"):
		return true
	}
	// Both terminals export their marker to whatever they launch, so a terminal
	// that claims TERM_PROGRAM for itself is not the one that set it.
	if os.Getenv("TERM_PROGRAM") != "" {
		return false
	}
	return os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != ""
}

// Image is a virtual placement: ID names the stored pixels, scaled to a grid
// of Columns x Rows cells. IDs stay under 1<<24 so the placeholder foreground
// carries them whole, and both dimensions under 297, the diacritics' range.
type Image struct {
	ID, Columns, Rows int
}

// Transmit stores and scales a PNG, sent as is: the terminal decodes it. A
// virtual placement paints nothing until placeholder cells appear, so this
// write is invisible and its cursor position irrelevant — which is what makes
// tea.Raw safe here.
func (i Image) Transmit(png []byte) tea.Cmd {
	data := []byte(base64.StdEncoding.EncodeToString(png))
	var buf strings.Builder
	for first := true; first || len(data) > 0; first = false {
		chunk := data[:min(len(data), 4096)]
		data = data[len(chunk):]
		more := "m=0"
		if len(data) > 0 {
			more = "m=1"
		}
		// q=1 on every chunk suppresses the OK but keeps errors: a rejected
		// image would otherwise leave placeholder cells with nothing behind them.
		if !first {
			buf.WriteString(ansi.KittyGraphics(chunk, "q=1", more))
			continue
		}
		buf.WriteString(ansi.KittyGraphics(chunk, append([]string{"a=T", "f=100", "t=d"}, append(i.placement(), more)...)...))
	}
	return tea.Raw(buf.String())
}

// Place resizes the stored image's placement without sending pixels again.
func (i Image) Place() tea.Cmd {
	return tea.Raw(ansi.KittyGraphics(nil, append([]string{"a=p"}, i.placement()...)...))
}

// placement keys a single virtual placement per image, so placing again
// replaces it.
func (i Image) placement() []string {
	return []string{"U=1", "q=1", "i=" + strconv.Itoa(i.ID), "p=1", "c=" + strconv.Itoa(i.Columns), "r=" + strconv.Itoa(i.Rows)}
}

// Delete frees the images' stored pixels in a single write, so it can be
// sequenced before the program stops writing.
func Delete(images ...Image) tea.Cmd {
	if len(images) == 0 {
		return nil
	}
	var buf strings.Builder
	for _, i := range images {
		buf.WriteString(ansi.KittyGraphics(nil, "a=d", "d=I", "i="+strconv.Itoa(i.ID), "q=2"))
	}
	return tea.Raw(buf.String())
}

// Placeholder returns every row of cells.
func (i Image) Placeholder() string {
	rows := make([]string, i.Rows)
	for y := range rows {
		rows[y] = i.Row(y, 0, i.Columns, lipgloss.NewStyle())
	}
	return strings.Join(rows, "\n")
}

// Row returns cells [from, to) of row y, in base with the image ID as their
// foreground. Each cell names its own row and column, so a row can be split
// around other content and still paint the right pixels. The terminal fits
// the picture inside the cells without stretching it: base's background shows
// wherever the picture does not cover them.
func (i Image) Row(y, from, to int, base lipgloss.Style) string {
	if from >= to {
		return ""
	}
	var row strings.Builder
	for x := from; x < to; x++ {
		row.WriteRune(kitty.Placeholder)
		row.WriteRune(kitty.Diacritic(y))
		row.WriteRune(kitty.Diacritic(x))
	}
	return base.Foreground(lipgloss.Color(fmt.Sprintf("#%06X", i.ID))).Render(row.String())
}

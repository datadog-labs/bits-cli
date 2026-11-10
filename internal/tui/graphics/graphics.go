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

// Support is what the terminal is known to paint. Unsupported is final.
type Support uint8

const (
	Unknown Support = iota
	Supported
	Unsupported
)

// Detection combines protocol reachability with a terminal identity known to
// paint Unicode placeholders. Its zero value is ready to query.
type Detection struct {
	Support
	queried, probeOK bool
	force, fallback  bool
	terminal         string
}

// Query asks for graphics reachability and XTVERSION in one write, followed by
// DA1. Its reply settles both ignored queries without a timeout. BITS_GRAPHICS
// defaults to auto; on bypasses identity checks, and off skips the queries.
// Multiplexers stay disabled even with on.
func (d *Detection) Query() tea.Cmd {
	if d.queried || d.Support != Unknown {
		return nil
	}
	d.queried = true
	mode := os.Getenv("BITS_GRAPHICS")
	if mode == "off" || multiplexed() {
		d.Support = Unsupported
		return nil
	}
	d.force = mode == "on"
	d.fallback = paintsPlaceholders()
	return tea.Raw(ansi.KittyGraphics(
		[]byte("AAAA"),
		"i="+strconv.Itoa(ProbeID), "s=1", "v=1", "a=q", "t=d", "f=24",
	) + ansi.RequestNameVersion + ansi.RequestPrimaryDeviceAttributes)
}

// Observe records a graphics reply. An OK probe still needs an identity check
// at DA1: a terminal can answer OK without painting placeholders.
// Only the probe and the canary, an image every session stores, speak for the
// terminal: another image's error is that image's own problem.
func (d *Detection) Observe(event uv.KittyGraphicsEvent, canary int) {
	ok := string(event.Payload) == "OK"
	switch id := event.Options.ID; {
	case !ok && (id == ProbeID || id == canary):
		d.Support = Unsupported
	case ok && id == ProbeID && d.Support == Unknown:
		d.probeOK = true
	}
}

// ObserveTerminalVersion records XTVERSION, which takes precedence over
// inherited environment markers. A multiplexer identity also vetoes on.
func (d *Detection) ObserveTerminalVersion(version string) {
	d.terminal = version
	switch terminalName(version) {
	case "tmux", "screen", "zellij":
		d.Support = Unsupported
	}
}

// ObserveDeviceAttributes settles the probe and identity queries, and reports
// whether images should now be transmitted. Replies to both queries precede
// DA1; absent XTVERSION, only the environment can identify the renderer.
func (d *Detection) ObserveDeviceAttributes() bool {
	if d.Support != Unknown {
		return false
	}
	paints := d.fallback
	if d.terminal != "" {
		paints = knownPlaceholderTerminal(d.terminal)
	}
	if d.probeOK && (d.force || paints) {
		d.Support = Supported
		return true
	}
	d.Support = Unsupported
	return false
}

// ObserveColorProfile rules out a terminal not driven in truecolor:
// placeholders find their image through an exact 24-bit foreground color,
// which a downsampled profile would lose or change.
func (s *Support) ObserveColorProfile(p colorprofile.Profile) {
	if p < colorprofile.TrueColor {
		*s = Unsupported
	}
}

// paintsPlaceholders is the fallback when XTVERSION is ignored. Answering a=q
// does not imply placeholder support: partial implementations can ignore U=1
// and draw U+10EEEE as an unknown glyph.
func paintsPlaceholders() bool {
	term := os.Getenv("TERM")
	if multiplexed() {
		return false
	}
	switch {
	case strings.Contains(term, "kitty"):
		return true
	case term == "xterm-ghostty", knownPlaceholderTerminal(os.Getenv("TERM_PROGRAM")):
		return true
	}
	// Both terminals export their marker to whatever they launch, so a terminal
	// that claims TERM_PROGRAM for itself is not the one that set it.
	if os.Getenv("TERM_PROGRAM") != "" {
		return false
	}
	return os.Getenv("KITTY_WINDOW_ID") != "" || os.Getenv("GHOSTTY_RESOURCES_DIR") != ""
}

func multiplexed() bool {
	term := os.Getenv("TERM")
	// A multiplexer does not forward placements, yet the outer terminal may
	// still answer the query.
	if os.Getenv("TMUX") != "" || os.Getenv("STY") != "" || os.Getenv("ZELLIJ") != "" ||
		os.Getenv("ZELLIJ_SESSION_NAME") != "" || strings.HasPrefix(term, "screen") ||
		strings.HasPrefix(term, "tmux") || strings.HasPrefix(term, "zellij") {
		return true
	}
	switch terminalName(os.Getenv("TERM_PROGRAM")) {
	case "tmux", "screen", "zellij":
		return true
	}
	return false
}

// XTVERSION uses both name(version) and name version forms. Match the name
// exactly so an unrelated terminal cannot qualify by including "kitty".
func terminalName(version string) string {
	fields := strings.Fields(strings.ToLower(version))
	if len(fields) == 0 {
		return ""
	}
	name, _, _ := strings.Cut(fields[0], "(")
	return name
}

func knownPlaceholderTerminal(version string) bool {
	switch terminalName(version) {
	case "kitty", "ghostty", "rex":
		return true
	}
	return false
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

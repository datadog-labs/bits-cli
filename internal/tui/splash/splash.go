// Package splash supplies the startup logo: the Bits dog as a raster image on
// terminals that speak the Kitty graphics protocol, the BITS wordmark as text
// elsewhere.
//
// Image bytes cannot travel in a Bubble Tea view — ultraviolet's printString
// parses content into cells and drops unrecognized escape sequences. Hence
// virtual placement: pixels transmit out of band through tea.Raw, and the view
// holds only printable placeholder cells marking where to paint them.
//
// Both forms are Rows tall, so the probe's asynchronous answer never changes
// the caller's layout.
package splash

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"strconv"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

//go:embed assets/logo.png
var logoPNG []byte

// Rows is the logo slot's height; callers reserve it without knowing which
// form will be drawn.
const Rows = 6

// Columns is the image form's width. Cells are about twice as tall as wide, so
// Columns x Rows is roughly square and the square logo is not stretched. The
// wordmark is wider, so a width gate must take the wider of the two.
const Columns = 12

const (
	// ImageID is re-encoded as the placeholder cells' foreground color, which
	// is how the terminal resolves a placeholder to a stored image. Under 1<<24
	// so the color carries it whole, without the extra diacritic.
	ImageID = 0x0B1754

	// ProbeID is distinct from ImageID so a probe reply cannot disturb the logo.
	ProbeID = 31
)

// Sourced from the Bits CLI's AsciiArt.ts. 28 columns by Rows rows.
const wordmark = "" +
	"██████╗ ██╗████████╗███████╗\n" +
	"██╔══██╗██║╚══██╔══╝██╔════╝\n" +
	"██████╔╝██║   ██║   ███████╗\n" +
	"██╔══██╗██║   ██║   ╚════██║\n" +
	"██████╔╝██║   ██║   ███████║\n" +
	"╚═════╝ ╚═╝   ╚═╝   ╚══════╝"

// Wordmark returns the unstyled wordmark.
func Wordmark() string { return wordmark }

// Query probes for placeholder support, or returns nil on a terminal that must
// not be asked. Capable terminals answer; others stay silent, so there is
// nothing to time out.
func Query() tea.Cmd {
	if !paintsPlaceholders() {
		return nil
	}
	return tea.Raw(ansi.KittyGraphics(
		[]byte("AAAA"),
		"i="+strconv.Itoa(ProbeID), "s=1", "v=1", "a=q", "t=d", "f=24",
	))
}

// ProbeSucceeded reports whether a reply is this package's probe answering OK.
// A failed query names an error instead, and another id is someone else's image.
func ProbeSucceeded(event uv.KittyGraphicsEvent) bool {
	return event.Options.ID == ProbeID && string(event.Payload) == "OK"
}

// ImageRejected reports whether the logo itself failed to store. Placeholder
// cells over a missing image paint nothing.
func ImageRejected(event uv.KittyGraphicsEvent) bool {
	return event.Options.ID == ImageID && string(event.Payload) != "OK"
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

// Transmit stores and scales the logo under ImageID. A virtual placement
// paints nothing until placeholder cells appear, so this write is invisible and
// its cursor position irrelevant — which is what makes tea.Raw safe here.
func Transmit() tea.Cmd {
	img, _, err := image.Decode(bytes.NewReader(logoPNG))
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	if err := kitty.EncodeGraphics(&buf, img, &kitty.Options{
		Action:           kitty.TransmitAndPut,
		Transmission:     kitty.Direct,
		Format:           kitty.PNG,
		ID:               ImageID,
		Columns:          Columns,
		Rows:             Rows,
		VirtualPlacement: true,
		Chunk:            true,
		// Suppress the OK but keep errors: a rejected image would otherwise
		// leave placeholder cells with nothing behind them.
		Quiet: 1,
	}); err != nil {
		return nil
	}
	return tea.Raw(buf.String())
}

// Placeholder returns the cells telling the terminal where to paint the image:
// the placeholder rune with row and column diacritics, over a foreground
// naming ImageID.
func Placeholder() string { return placeholder() }

var placeholder = sync.OnceValue(func() string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%06X", ImageID)))
	rows := make([]string, Rows)
	for y := range rows {
		var row strings.Builder
		for x := range Columns {
			row.WriteRune(kitty.Placeholder)
			row.WriteRune(kitty.Diacritic(y))
			row.WriteRune(kitty.Diacritic(x))
		}
		rows[y] = style.Render(row.String())
	}
	return strings.Join(rows, "\n")
})

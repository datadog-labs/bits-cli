// Package splash supplies the startup logo: the Bits dog as a raster image on
// terminals that speak the Kitty graphics protocol, the BITS wordmark as text
// elsewhere.
//
// Image bytes cannot travel in a Bubble Tea view — ultraviolet's printString
// parses content into cells and drops unrecognized escape sequences. Hence
// virtual placement: pixels transmit out of band through tea.Raw, and the view
// holds only printable placeholder cells marking where to paint them.
//
// Support needs two signals: the terminal must be on an allowlist known to
// paint Unicode placeholders, and it must answer the graphics query. Neither
// alone is sufficient — see paintsPlaceholders.
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

// Cells are about twice as tall as wide, so imageCols x Rows is roughly square
// and the square logo is not stretched.
const imageCols = 12

const (
	// ImageID is re-encoded as the placeholder cells' foreground color, which
	// is how the terminal resolves a placeholder to a stored image. Under
	// 1<<24 so the color carries the whole id and the most-significant-byte
	// diacritic can be omitted.
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

// Query transmits a 1x1 pixel and requests a response, or returns nil on a
// terminal that must not be asked. Supporting terminals answer with an
// ultraviolet.KittyGraphicsEvent; others stay silent, so there is nothing to
// time out.
func Query() tea.Cmd {
	if !paintsPlaceholders() {
		return nil
	}
	return tea.Raw(ansi.KittyGraphics(
		[]byte("AAAA"),
		"i="+strconv.Itoa(ProbeID), "s=1", "v=1", "a=q", "t=d", "f=24",
	))
}

// ProbeSucceeded reports whether a graphics reply is this package's probe
// answering OK. A failed query answers with an error name in place of OK, and
// a reply carrying another id belongs to someone else's image.
func ProbeSucceeded(event uv.KittyGraphicsEvent) bool {
	return event.Options.ID == ProbeID && string(event.Payload) == "OK"
}

// ImageRejected reports whether a reply says the logo itself failed to store.
// Placeholder cells over a missing image paint nothing, so the caller must fall
// back to the wordmark.
func ImageRejected(event uv.KittyGraphicsEvent) bool {
	return event.Options.ID == ImageID && string(event.Payload) != "OK"
}

// paintsPlaceholders reports whether the terminal is known to paint Unicode
// placeholders, the narrowest capability in the graphics protocol and the one
// with no query of its own. Answering the graphics query does not imply it:
// iTerm2 answers, accepts a virtual placement, ignores the U key, and renders
// the placeholder rune as an unknown glyph. So the reply proves only that the
// protocol is reachable — tmux and ssh can swallow it — and this allowlist
// carries the rest.
func paintsPlaceholders() bool {
	term := os.Getenv("TERM")
	// A multiplexer does not forward placements, yet the outer terminal may
	// still answer the query — which would leave blank cells.
	if os.Getenv("TMUX") != "" || os.Getenv("STY") != "" ||
		strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux") {
		return false
	}
	switch {
	case strings.Contains(term, "kitty"), os.Getenv("KITTY_WINDOW_ID") != "":
		return true
	case term == "xterm-ghostty", strings.EqualFold(os.Getenv("TERM_PROGRAM"), "ghostty"):
		return true
	}
	// Ghostty's resource path is inherited by anything it launches, including
	// other terminals, so it only counts while no other terminal claims TERM_PROGRAM.
	return os.Getenv("GHOSTTY_RESOURCES_DIR") != "" && os.Getenv("TERM_PROGRAM") == ""
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
		Columns:          imageCols,
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

// Placeholder returns the cells that tell the terminal where to paint the
// transmitted image: the placeholder rune plus row and column diacritics, over
// a foreground naming ImageID.
func Placeholder() string { return placeholder() }

var placeholder = sync.OnceValue(func() string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(fmt.Sprintf("#%06X", ImageID)))
	rows := make([]string, Rows)
	for y := range rows {
		var row strings.Builder
		for x := range imageCols {
			row.WriteRune(kitty.Placeholder)
			row.WriteRune(kitty.Diacritic(y))
			row.WriteRune(kitty.Diacritic(x))
		}
		rows[y] = style.Render(row.String())
	}
	return strings.Join(rows, "\n")
})

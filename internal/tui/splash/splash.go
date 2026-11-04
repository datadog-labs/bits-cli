// Package splash supplies the startup logo: the Bits dog as a raster image on
// terminals that paint Kitty graphics, the BITS wordmark as text elsewhere.
//
// Both forms are Rows tall, so the probe's asynchronous answer never changes
// the caller's layout.
package splash

import (
	_ "embed"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/tui/graphics"
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

// ImageID is the logo's stored image.
const ImageID = 0x0B1754

var logo = graphics.Image{ID: ImageID, Columns: Columns, Rows: Rows}

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

// Transmit stores and scales the logo.
func Transmit() tea.Cmd { return logo.Transmit(logoPNG) }

// Placeholder returns the cells telling the terminal where to paint the logo.
func Placeholder() string { return placeholder() }

var placeholder = sync.OnceValue(logo.Placeholder)

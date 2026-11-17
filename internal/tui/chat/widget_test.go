package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/graphics"
)

// The band spans the full width with the image centered in it. The badge
// replaces only the band's center cells: every image cell must still address
// its own column, or the picture shifts around the badge. The padded title
// row heads the band.
func TestWidgetBadgeCentersOverImage(t *testing.T) {
	sty := DefaultStyles(true)
	band := 80
	img := graphics.Image{ID: 0xB10001, Columns: 40, Rows: 9}
	left := (band - img.Columns) / 2
	for name, view := range map[string]WidgetView{
		"rendering": {Title: "CPU", Image: graphics.Image{Columns: img.Columns, Rows: img.Rows}, Loading: "Rendering"},
		"opening":   {Title: "CPU", Image: img, Loading: "Opening"},
	} {
		t.Run(name, func(t *testing.T) {
			lines := strings.Split(ansi.Strip(renderWidgetView(view, renderContext{width: 80, sty: sty})), "\n")
			box := lines[1 : 1+img.Rows]
			for y, row := range box {
				if w := ansi.StringWidth(row); w != 80 {
					t.Fatalf("row %d is %d columns, want the full width", y, w)
				}
			}
			// The title row is the card's header: padded, as wide as the band.
			if !strings.HasPrefix(lines[0], widgetTitlePad+"CPU") || ansi.StringWidth(lines[0]) != 80 {
				t.Fatalf("title row %q is not a padded full-width header", lines[0])
			}
			middle := box[img.Rows/2]
			if at := ansi.StringWidth(middle[:strings.Index(middle, view.Loading)]); at < band/2-len(view.Loading)-4 || at > band/2 {
				t.Fatalf("badge not centered in %q", middle)
			}
			if view.Image.ID == 0 {
				return
			}
			// Walk band columns: each placeholder cell names column x-left.
			for _, row := range []string{box[0], middle} {
				cells, x, first, last := []rune(row), 0, -1, -1
				for i := 0; i < len(cells); x++ {
					if cells[i] != kitty.Placeholder {
						i++
						continue
					}
					if cells[i+2] != kitty.Diacritic(x-left) {
						t.Fatalf("band column %d does not address image column %d", x, x-left)
					}
					if first < 0 {
						first = x
					}
					last = x
					i += 3
				}
				if row == box[0] && (first != left || last != left+img.Columns-1) {
					t.Fatalf("image spans %d..%d, want centered at %d..%d", first, last, left, left+img.Columns-1)
				}
			}
		})
	}

	// Too narrow for the label: the spinner alone still shows progress.
	narrow := renderWidgetView(WidgetView{Image: graphics.Image{Columns: 5, Rows: 3}, Loading: "Rendering"}, renderContext{width: 5, sty: sty})
	if lines := strings.Split(ansi.Strip(narrow), "\n"); !strings.Contains(lines[2], sty.StatusSpinner.Frame(0)) {
		t.Fatalf("narrow box lost its spinner:\n%s", ansi.Strip(narrow))
	}

	// A long title gives way before the action's icon does.
	title := strings.Split(ansi.Strip(renderWidgetView(WidgetView{Title: strings.Repeat("CPU ", 30), Action: WidgetOpen}, renderContext{width: 40, sty: sty})), "\n")[0]
	if !strings.HasSuffix(strings.TrimRight(title, " "), "…"+widgetIcons[WidgetOpen]) || ansi.StringWidth(title) > 40 {
		t.Fatalf("title %q lost its icon or overflowed", title)
	}
}

// Until the widgets component gives an app a view, as after a logout, the
// transcript still names it.
func TestAppWithoutViewShowsItsTitle(t *testing.T) {
	result := json.RawMessage(`{"structuredContent":{"widget_definition":{"type":"timeseries","title":"CPU"}}}`)
	for name, tc := range map[string]struct {
		app  assistant.AppPayload
		want string
	}{
		"titled": {assistant.AppPayload{Result: result}, "[view: CPU]"},
		"saved":  {assistant.AppPayload{Restorable: true}, "[view]"},
	} {
		t.Run(name, func(t *testing.T) {
			list := NewList()
			list.SetStyles(DefaultStyles(true))
			list.SetWidth(60)
			list.SetHeight(10)
			list.SetItems([]agent.Block{{ID: agent.BlockID{Key: "w"}, Kind: assistant.KindApp, Complete: true, App: &tc.app}})
			if doc := ansi.Strip(list.Document()); !strings.Contains(doc, tc.want) {
				t.Fatalf("document:\n%s", doc)
			}
		})
	}
}

// Status lines under the card line up with its title.
func TestWidgetStatusLinesAlignWithTitle(t *testing.T) {
	for name, view := range map[string]WidgetView{
		"note":    {Title: "CPU", Note: "Invalid query"},
		"loading": {Title: "CPU", Loading: "Opening"},
	} {
		t.Run(name, func(t *testing.T) {
			lines := strings.Split(ansi.Strip(renderWidgetView(view, renderContext{width: 60, sty: DefaultStyles(true)})), "\n")
			if last := lines[len(lines)-1]; !strings.HasPrefix(last, widgetTitlePad) || last[len(widgetTitlePad)] == ' ' {
				t.Fatalf("status line %q is not aligned with the title", last)
			}
		})
	}
}

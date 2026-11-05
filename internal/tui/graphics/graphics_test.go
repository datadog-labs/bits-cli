package graphics

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// Answering the graphics query does not imply placeholder support, so real
// terminal environments are pinned here. Every case is a terminal someone runs.
func TestPaintsPlaceholders(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"kitty":            {env: map[string]string{"TERM": "xterm-kitty"}, want: true},
		"kitty via id":     {env: map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "1"}, want: true},
		"ghostty":          {env: map[string]string{"TERM": "xterm-ghostty"}, want: true},
		"ghostty via prog": {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "ghostty"}, want: true},
		// cmux embeds Ghostty but leaves TERM generic and TERM_PROGRAM unset, so
		// the resource path is the only signal.
		"cmux": {env: map[string]string{"TERM": "xterm-256color", "GHOSTTY_RESOURCES_DIR": "/Applications/cmux.app/Contents/Resources/ghostty"}, want: true},

		"iterm2":         {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app"}, want: false},
		"terminal.app":   {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Apple_Terminal"}, want: false},
		"bare xterm":     {env: map[string]string{"TERM": "xterm-256color"}, want: false},
		"no term at all": {env: map[string]string{}, want: false},
		// A multiplexer cannot paint a placement, but the outer terminal may
		// still answer the query.
		"ghostty inside tmux": {env: map[string]string{
			"TERM": "tmux-256color", "TMUX": "/private/tmp/tmux-501/default,123,0",
			"GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app/Contents/Resources/ghostty",
		}, want: false},
		"kitty inside tmux": {env: map[string]string{
			"TERM": "xterm-kitty", "TMUX": "/private/tmp/tmux-501/default,123,0",
		}, want: false},
		"kitty inside screen": {env: map[string]string{
			"TERM": "screen.xterm-kitty", "STY": "1234.pts-0.host",
		}, want: false},

		// Ghostty exports its resource path to children, so a terminal launched
		// from it inherits the variable without inheriting the capability.
		"iterm2 launched from ghostty": {env: map[string]string{
			"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app",
			"GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app/Contents/Resources/ghostty",
		}, want: false},
		"iterm2 launched from kitty": {env: map[string]string{
			"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app", "KITTY_WINDOW_ID": "1",
		}, want: false},
		// Nothing claims TERM_PROGRAM, so the inherited marker is kitty's own.
		"kitty with a rewritten term": {env: map[string]string{
			"TERM": "xterm-256color", "KITTY_WINDOW_ID": "1",
		}, want: true},
	} {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"TERM", "TERM_PROGRAM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "TMUX", "STY"} {
				t.Setenv(key, tc.env[key])
			}
			if got := paintsPlaceholders(); got != tc.want {
				t.Errorf("paintsPlaceholders() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Query's outcome must come from the pinned environment, never from whichever
// terminal happens to be running the tests.
func TestQueryFollowsTerminalCapability(t *testing.T) {
	for name, tc := range map[string]struct {
		env   map[string]string
		probe bool
	}{
		"kitty":  {env: map[string]string{"TERM": "xterm-kitty"}, probe: true},
		"iterm2": {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app"}},
		"ci":     {env: map[string]string{"TERM": "dumb"}},
	} {
		t.Run(name, func(t *testing.T) {
			// TMUX and STY included: a run inside a multiplexer would otherwise
			// inherit them and suppress the probe.
			for _, key := range []string{"TERM", "TERM_PROGRAM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "TMUX", "STY"} {
				t.Setenv(key, tc.env[key])
			}
			if probed := Query() != nil; probed != tc.probe {
				t.Errorf("Query() probed = %v, want %v", probed, tc.probe)
			}
		})
	}
}

// Large images go out in chunks: only the first carries the placement, every
// chunk stays quiet, and the payload reassembles to the PNG as sent.
func TestTransmitChunks(t *testing.T) {
	png := make([]byte, 5000)
	for i := range png {
		png[i] = byte(i)
	}
	raw := Image{ID: 7, Columns: 40, Rows: 10}.Transmit(png)().(tea.RawMsg).Msg.(string)
	chunks := strings.Split(strings.TrimSuffix(raw, "\x1b\\"), "\x1b\\")
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	var payload strings.Builder
	for i, chunk := range chunks {
		keys, data, _ := strings.Cut(strings.TrimPrefix(chunk, "\x1b_G"), ";")
		last := i == len(chunks)-1
		if !strings.Contains(keys, "q=1") || strings.Contains(keys, "m=0") != last {
			t.Fatalf("chunk %d keys %q", i, keys)
		}
		if first := strings.Contains(keys, "a=T,f=100,t=d,U=1,q=1,i=7,p=1,c=40,r=10"); first != (i == 0) {
			t.Fatalf("chunk %d placement keys %q", i, keys)
		}
		payload.WriteString(data)
	}
	if got, err := base64.StdEncoding.DecodeString(payload.String()); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("payload does not reassemble: %v", err)
	}
}

// The terminal resolves each cell through its foreground color and diacritics
// alone, so a split row must keep both on every cell it emits.
func TestRowCellsCarryImageRowAndColumn(t *testing.T) {
	img := Image{ID: 0x0B1754, Columns: 12, Rows: 6}
	color := "38;2;" + strconv.Itoa(0x0B) + ";" + strconv.Itoa(0x17) + ";" + strconv.Itoa(0x54)
	for y, line := range strings.Split(img.Placeholder(), "\n") {
		if got := ansi.StringWidth(line); got != img.Columns {
			t.Fatalf("row %d measures %d columns, want %d", y, got, img.Columns)
		}
		if !strings.Contains(line, color) {
			t.Fatalf("row %d does not set foreground %q", y, color)
		}
	}
	segment := []rune(ansi.Strip(img.Row(2, 4, 7, lipgloss.NewStyle())))
	if len(segment) != 3*3 {
		t.Fatalf("segment has %d runes, want 3 cells", len(segment))
	}
	for i := range 3 {
		if segment[3*i] != kitty.Placeholder || segment[3*i+1] != kitty.Diacritic(2) || segment[3*i+2] != kitty.Diacritic(4+i) {
			t.Fatalf("cell %d is not addressed to row 2, column %d", i, 4+i)
		}
	}
	// The base background fills what the fitted picture leaves uncovered.
	if on := img.Row(0, 0, 1, lipgloss.NewStyle().Background(lipgloss.Color("#010203"))); !strings.Contains(on, "48;2;1;2;3") || !strings.Contains(on, color) {
		t.Fatalf("cell lost its background or image ID: %q", on)
	}
	if img.Row(0, 5, 5, lipgloss.NewStyle()) != "" {
		t.Fatal("an empty range emitted cells")
	}
}

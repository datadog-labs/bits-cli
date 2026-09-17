package splash

import (
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

// The probe answers asynchronously, so the form can change after the first
// paint; differing heights would make the caller's block jump.
func TestBothFormsAreExactlyRowsTall(t *testing.T) {
	for name, form := range map[string]string{"wordmark": Wordmark(), "placeholder": Placeholder()} {
		if lines := strings.Count(form, "\n") + 1; lines != Rows {
			t.Errorf("%s is %d lines, want %d", name, lines, Rows)
		}
	}
}

// A ragged line would break the horizontal join with the fact column.
func TestWordmarkIsRectangular(t *testing.T) {
	lines := strings.Split(Wordmark(), "\n")
	want := ansi.StringWidth(lines[0])
	for i, line := range lines {
		if got := ansi.StringWidth(line); got != want {
			t.Errorf("line %d is %d columns wide, want %d", i, got, want)
		}
	}
}

// The terminal resolves a placeholder through this color alone, so a wrong or
// downsampled value paints nothing.
func TestPlaceholderCarriesImageIDAsForeground(t *testing.T) {
	want := "38;2;" +
		strconv.Itoa((imageID>>16)&0xFF) + ";" +
		strconv.Itoa((imageID>>8)&0xFF) + ";" +
		strconv.Itoa(imageID&0xFF)
	if !strings.Contains(Placeholder(), want) {
		t.Fatalf("grid does not set foreground %q", want)
	}
}

// Every cell is addressed explicitly rather than relying on the terminal's
// guess-from-previous-cell fallback.
func TestPlaceholderCellsCarryRowAndColumn(t *testing.T) {
	for y, line := range strings.Split(Placeholder(), "\n") {
		if cells := strings.Count(line, string(kitty.Placeholder)); cells != imageCols {
			t.Fatalf("row %d has %d placeholder cells, want %d", y, cells, imageCols)
		}
		if !strings.ContainsRune(line, kitty.Diacritic(y)) {
			t.Fatalf("row %d is missing its row diacritic", y)
		}
		for x := range imageCols {
			if !strings.ContainsRune(line, kitty.Diacritic(x)) {
				t.Fatalf("row %d is missing the column diacritic for cell %d", y, x)
			}
		}
	}
}

// The private-use placeholder rune's measured width is what the horizontal
// join relies on.
func TestPlaceholderOccupiesImageColsColumns(t *testing.T) {
	for y, line := range strings.Split(Placeholder(), "\n") {
		if got := ansi.StringWidth(line); got != imageCols {
			t.Errorf("row %d measures %d columns, want %d", y, got, imageCols)
		}
	}
}

func TestQueryAndTransmitProduceCommands(t *testing.T) {
	if Query() == nil {
		t.Fatal("Query returned no command")
	}
	if Transmit() == nil {
		t.Fatal("Transmit returned no command; the embedded logo failed to decode")
	}
}

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
		// Ghostty exports its resource path to children, so a terminal launched
		// from it inherits the variable without inheriting the capability.
		"iterm2 launched from ghostty": {env: map[string]string{
			"TERM": "xterm-256color", "TERM_PROGRAM": "iTerm.app",
			"GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app/Contents/Resources/ghostty",
		}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"TERM", "TERM_PROGRAM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR"} {
				t.Setenv(key, tc.env[key])
			}
			if got := paintsPlaceholders(); got != tc.want {
				t.Errorf("paintsPlaceholders() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQuerySkipsTerminalsWithoutPlaceholders(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	t.Setenv("KITTY_WINDOW_ID", "")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "")
	if Query() != nil {
		t.Fatal("queried a terminal that cannot paint placeholders")
	}
}

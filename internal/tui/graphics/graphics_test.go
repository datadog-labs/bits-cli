package graphics

import (
	"bytes"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
)

func terminalEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, key := range []string{"TERM", "TERM_PROGRAM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "TMUX", "STY", "ZELLIJ", "ZELLIJ_SESSION_NAME", "BITS_GRAPHICS"} {
		t.Setenv(key, env[key])
	}
}

// Answering the graphics query does not imply placeholder support, so real
// terminal environments are pinned here. Every case is a terminal someone runs.
func TestPaintsPlaceholders(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"rex":              {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "rex"}, want: true},
		"kitty via prog":   {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "kitty"}, want: true},
		"kitty":            {env: map[string]string{"TERM": "xterm-kitty"}, want: true},
		"kitty via id":     {env: map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "1"}, want: true},
		"ghostty":          {env: map[string]string{"TERM": "xterm-ghostty"}, want: true},
		"ghostty via prog": {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "ghostty"}, want: true},
		// cmux embeds Ghostty but leaves TERM generic and TERM_PROGRAM unset, so
		// the resource path is the only signal.
		"cmux": {env: map[string]string{"TERM": "xterm-256color", "GHOSTTY_RESOURCES_DIR": "/Applications/cmux.app/Contents/Resources/ghostty"}, want: true},

		"wezterm":        {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "WezTerm"}, want: false},
		"konsole":        {env: map[string]string{"TERM": "xterm-256color", "TERM_PROGRAM": "Konsole"}, want: false},
		"zellij":         {env: map[string]string{"TERM": "xterm-kitty", "ZELLIJ": "0"}, want: false},
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
			terminalEnv(t, tc.env)
			if got := paintsPlaceholders(); got != tc.want {
				t.Errorf("paintsPlaceholders() = %v, want %v", got, tc.want)
			}
		})
	}
}

// An unknown environment must still be probed: XTVERSION may identify the
// terminal across SSH, where terminal-specific environment markers are lost.
func TestQuery(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		skip bool
	}{
		"unknown environment":        {},
		"kitty":                      {env: map[string]string{"TERM": "xterm-kitty"}},
		"rex":                        {env: map[string]string{"TERM_PROGRAM": "rex"}},
		"iterm2":                     {env: map[string]string{"TERM_PROGRAM": "iTerm.app"}},
		"auto":                       {env: map[string]string{"BITS_GRAPHICS": "auto"}},
		"on":                         {env: map[string]string{"BITS_GRAPHICS": "on"}},
		"off":                        {env: map[string]string{"BITS_GRAPHICS": "off", "TERM": "xterm-kitty"}, skip: true},
		"invalid falls back to auto": {env: map[string]string{"BITS_GRAPHICS": "yes"}},
		"tmux":                       {env: map[string]string{"TMUX": "/tmp/tmux", "BITS_GRAPHICS": "on"}, skip: true},
		"screen":                     {env: map[string]string{"STY": "1234.tty", "BITS_GRAPHICS": "on"}, skip: true},
		"zellij":                     {env: map[string]string{"ZELLIJ": "0", "BITS_GRAPHICS": "on"}, skip: true},
		"zellij session":             {env: map[string]string{"ZELLIJ_SESSION_NAME": "work", "BITS_GRAPHICS": "on"}, skip: true},
		"tmux term":                  {env: map[string]string{"TERM": "tmux-256color", "BITS_GRAPHICS": "on"}, skip: true},
		"screen term":                {env: map[string]string{"TERM": "screen.xterm-kitty", "BITS_GRAPHICS": "on"}, skip: true},
		"zellij term":                {env: map[string]string{"TERM": "zellij", "BITS_GRAPHICS": "on"}, skip: true},
		"zellij program":             {env: map[string]string{"TERM_PROGRAM": "zellij", "BITS_GRAPHICS": "on"}, skip: true},
	} {
		t.Run(name, func(t *testing.T) {
			terminalEnv(t, tc.env)
			var d Detection
			cmd := d.Query()
			if (cmd == nil) != tc.skip {
				t.Fatalf("Query() = %v, skip = %v", cmd, tc.skip)
			}
			if tc.skip {
				if d.Support != Unsupported {
					t.Fatal("skipped probe did not settle support")
				}
			} else {
				want := ansi.KittyGraphics([]byte("AAAA"), "i=31", "s=1", "v=1", "a=q", "t=d", "f=24") + ansi.RequestNameVersion + ansi.RequestPrimaryDeviceAttributes
				if got := cmd().(tea.RawMsg).Msg; got != want {
					t.Fatalf("query order = %q, want %q", got, want)
				}
			}
			if d.Query() != nil {
				t.Fatal("repeated query sent another probe")
			}
		})
	}
}

func TestDetection(t *testing.T) {
	for name, tc := range map[string]struct {
		env     map[string]string
		version string
		reply   string
		want    Support
	}{
		"rex over ssh":                        {version: "rex 0.0.1793981048-g01d6c4", reply: "OK", want: Supported},
		"kitty":                               {version: "kitty(0.46.2)", reply: "OK", want: Supported},
		"ghostty":                             {version: "ghostty 1.3.1", reply: "OK", want: Supported},
		"case insensitive":                    {version: "Ghostty(1.3.1)", reply: "OK", want: Supported},
		"iterm2":                              {version: "iTerm2 3.6.9", reply: "OK", want: Unsupported},
		"wezterm":                             {version: "WezTerm 20240203-110809-5046fc22", reply: "OK", want: Unsupported},
		"konsole":                             {version: "Konsole 26.08.0", reply: "OK", want: Unsupported},
		"foot":                                {version: "foot(1.25.0)", reply: "OK", want: Unsupported},
		"unknown":                             {version: "future-terminal(1)", reply: "OK", want: Unsupported},
		"name must match":                     {version: "not-kitty(1)", reply: "OK", want: Unsupported},
		"no identity":                         {reply: "OK", want: Unsupported},
		"ignored protocol":                    {version: "rex 1", want: Unsupported},
		"rejected protocol":                   {version: "kitty(0.46.2)", reply: "EINVAL:bad key", want: Unsupported},
		"rex env fallback":                    {env: map[string]string{"TERM_PROGRAM": "rex"}, reply: "OK", want: Supported},
		"kitty env fallback":                  {env: map[string]string{"TERM": "xterm-kitty"}, reply: "OK", want: Supported},
		"cmux env fallback":                   {env: map[string]string{"GHOSTTY_RESOURCES_DIR": "/ghostty"}, reply: "OK", want: Supported},
		"identity overrides inherited marker": {env: map[string]string{"GHOSTTY_RESOURCES_DIR": "/ghostty"}, version: "iTerm2 3.6.9", reply: "OK", want: Unsupported},
		"identity overrides term":             {env: map[string]string{"TERM": "xterm-kitty"}, version: "WezTerm 1", reply: "OK", want: Unsupported},
		"identity overrides program":          {env: map[string]string{"TERM_PROGRAM": "iTerm.app"}, version: "rex 1", reply: "OK", want: Supported},
		"on bypasses identity":                {env: map[string]string{"BITS_GRAPHICS": "on"}, version: "future-terminal(1)", reply: "OK", want: Supported},
		"on without identity":                 {env: map[string]string{"BITS_GRAPHICS": "on"}, reply: "OK", want: Supported},
		"on still needs protocol":             {env: map[string]string{"BITS_GRAPHICS": "on"}, version: "rex 1", want: Unsupported},
		"on still respects errors":            {env: map[string]string{"BITS_GRAPHICS": "on"}, reply: "EINVAL:bad key", want: Unsupported},
		"off is final":                        {env: map[string]string{"BITS_GRAPHICS": "off"}, version: "rex 1", reply: "OK", want: Unsupported},
		"invalid means auto":                  {env: map[string]string{"BITS_GRAPHICS": "yes"}, reply: "OK", want: Unsupported},
		"tmux identity vetoes on":             {env: map[string]string{"BITS_GRAPHICS": "on"}, version: "tmux 3.6", reply: "OK", want: Unsupported},
		"screen identity vetoes on":           {env: map[string]string{"BITS_GRAPHICS": "on"}, version: "screen(5.0)", reply: "OK", want: Unsupported},
		"zellij identity vetoes on":           {env: map[string]string{"BITS_GRAPHICS": "on"}, version: "Zellij 0.43", reply: "OK", want: Unsupported},
	} {
		t.Run(name, func(t *testing.T) {
			terminalEnv(t, tc.env)
			var d Detection
			d.Query()
			if tc.reply != "" {
				d.Observe(uv.KittyGraphicsEvent{Options: kitty.Options{ID: ProbeID}, Payload: []byte(tc.reply)}, 42)
			}
			d.ObserveTerminalVersion(tc.version)
			if d.Support == Supported {
				t.Fatal("images enabled before DA1 settled identity")
			}
			if enabled := d.ObserveDeviceAttributes(); enabled != (tc.want == Supported) {
				t.Errorf("transmit = %v, want support %v", enabled, tc.want)
			}
			if d.Support != tc.want {
				t.Errorf("support = %v, want %v", d.Support, tc.want)
			}
			if d.ObserveDeviceAttributes() {
				t.Fatal("duplicate DA1 retransmitted images")
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

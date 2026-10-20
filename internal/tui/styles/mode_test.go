package styles

import "testing"

// TestModeDarkPinsPolarity is the whole point of the type: a pinned mode has to
// win over whatever the terminal reported, and auto has to defer to it.
func TestModeDarkPinsPolarity(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     Mode
		detected bool
		want     bool
	}{
		{name: "auto follows dark terminal", mode: ModeAuto, detected: true, want: true},
		{name: "auto follows light terminal", mode: ModeAuto, detected: false, want: false},
		{name: "unset behaves as auto", mode: "", detected: true, want: true},
		{name: "light overrides dark terminal", mode: ModeLight, detected: true, want: false},
		{name: "dark overrides light terminal", mode: ModeDark, detected: false, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.mode.Dark(test.detected); got != test.want {
				t.Errorf("Mode(%q).Dark(%t) = %t, want %t", test.mode, test.detected, got, test.want)
			}
		})
	}
}

func TestParseMode(t *testing.T) {
	for _, test := range []struct {
		raw     string
		want    Mode
		wantErr bool
	}{
		{raw: "auto", want: ModeAuto},
		{raw: "light", want: ModeLight},
		{raw: "dark", want: ModeDark},
		{raw: "", wantErr: true},
		{raw: "Light", wantErr: true},
		{raw: "solarized", wantErr: true},
	} {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseMode(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseMode(%q) = %q, want an error", test.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMode(%q) returned %v", test.raw, err)
			}
			if got != test.want {
				t.Errorf("ParseMode(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

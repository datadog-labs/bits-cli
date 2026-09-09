package styles

import (
	"math"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDefaultSelectsModeSpecificInputAndMenuStyles(t *testing.T) {
	dark := Default(true)
	light := Default(false)

	if !dark.IsDark || light.IsDark {
		t.Errorf("theme mode = dark:%t light:%t, want dark:true light:false", dark.IsDark, light.IsDark)
	}
	if dark.Input.Background == light.Input.Background {
		t.Error("input background should differ between themes")
	}
	if dark.Editor.MenuItem.GetBackground() == light.Editor.MenuItem.GetBackground() {
		t.Error("completion-menu background should differ between themes")
	}
	if got, want := dark.Input.ContentOffset(), dark.Input.PromptWidth(); got != want {
		t.Errorf("ContentOffset() = %d, want %d", got, want)
	}
}

// TestDefaultPinsAModeSpecificBackground covers the painted alt-screen
// background: both modes must supply one, and they must differ, since the whole
// point is that the app stops inheriting the terminal's own background.
func TestDefaultPinsAModeSpecificBackground(t *testing.T) {
	dark, light := Default(true), Default(false)

	if dark.Background == nil || light.Background == nil {
		t.Fatalf("background = dark:%v light:%v, want both set", dark.Background, light.Background)
	}
	if dark.Background == light.Background {
		t.Error("background should differ between themes")
	}
	if got, want := dark.Background, lipgloss.Color(darkPalette().background); got != want {
		t.Errorf("dark background = %v, want token %v", got, want)
	}
	if got, want := light.Background, lipgloss.Color(lightPalette().background); got != want {
		t.Errorf("light background = %v, want token %v", got, want)
	}
}

// TestSurfacesSeparateFromTheirBackground pins the elevation contract in both
// modes: every surface painted over the background has to be distinguishable
// from it, or the block it draws disappears into the page. Light mode regressed
// exactly this way when the background was pinned ahead of its ramp.
func TestSurfacesSeparateFromTheirBackground(t *testing.T) {
	// pendingElevation names roles that do not clear their background yet.
	//
	// TODO: dark's errorSurface is at 1.022:1 against the pinned page, so the
	// chip draws but its surface is indistinguishable from the transcript. It
	// cannot be fixed from this side alone: lifting the surface pushes the label
	// further under AA (see TestChipLabelsReadOnTheirOwnSurface), and dark's
	// status colors are deliberately held to what they were. Resolving it means
	// deciding whether the error chip keeps a surface at all — the same question
	// StatusSuccess already answered by dropping one.
	pendingElevation := map[string]map[string]bool{
		"dark": {"errorSurface": true},
	}
	for _, test := range []struct {
		name    string
		palette palette
	}{
		{name: "dark", palette: darkPalette()},
		{name: "light", palette: lightPalette()},
	} {
		t.Run(test.name, func(t *testing.T) {
			for role, surface := range map[string]string{
				"surface":         test.palette.surface,
				"surfaceRaised":   test.palette.surfaceRaised,
				"approvalSurface": test.palette.approvalSurface,
				"codeSurface":     test.palette.codeSurface,
				"errorSurface":    test.palette.errorSurface,
				"busySurface":     test.palette.busySurface,
			} {
				// Some dark roles are still raw ANSI indices, which carry no hex
				// luminance to measure; they are exempt until they join the ramp.
				if !strings.HasPrefix(surface, "#") {
					continue
				}
				if pendingElevation[test.name][role] {
					continue
				}
				if got := contrastRatio(surface, test.palette.background); got < 1.1 {
					t.Errorf("%s/background contrast = %.3f:1, want at least 1.1:1", role, got)
				}
			}
		})
	}
}

// TestElevationRunsAwayFromThePage guards the ramp's direction, which differs
// by mode: dark elevates by getting lighter, light by getting darker, since a
// 94%-lightness page has no headroom above it.
func TestElevationRunsAwayFromThePage(t *testing.T) {
	dark := darkPalette()
	if relativeLuminance(dark.surface) <= relativeLuminance(dark.background) {
		t.Error("dark surface should be lighter than the background it sits on")
	}
	light := lightPalette()
	if relativeLuminance(light.surface) >= relativeLuminance(light.background) {
		t.Error("light surface should be darker than the background it sits on")
	}
}

func TestForegroundTokensMeetNormalTextContrast(t *testing.T) {
	for _, test := range []struct {
		name    string
		palette palette
	}{
		{name: "dark", palette: darkPalette()},
		{name: "light", palette: lightPalette()},
	} {
		t.Run(test.name, func(t *testing.T) {
			for role, foreground := range map[string]string{
				"interactive":      test.palette.interactive,
				"muted":            test.palette.muted,
				"feedback success": test.palette.feedbackSuccess,
				"feedback error":   test.palette.feedbackError,
			} {
				if got := contrastRatio(foreground, test.palette.surface); got < 4.5 {
					t.Errorf("%s contrast = %.2f:1, want at least 4.5:1", role, got)
				}
			}
		})
	}
}

// TestChipLabelsReadOnTheirOwnSurface covers the pairs that carry both their
// foreground and their background from the palette — the status chips and code
// spans — so neither half can be moved without the other.
func TestChipLabelsReadOnTheirOwnSurface(t *testing.T) {
	// unreachableAA names pairs that cannot meet 4.5:1 without changing a color
	// that is deliberately fixed.
	//
	// dark's error pair is one. With error pinned at #D33043 the bound is not
	// merely unmet, it is unreachable: 4.5:1 would need an errorSurface of
	// negative luminance, and even pure black caps the pair at 4.27:1. Darkening
	// the surface is the only direction that helps at all (#2F0A0F gives 3.65:1,
	// black 4.27:1), and it trades away the elevation the chip needs to be
	// visible at all. So this is a palette decision, not an oversight — see the
	// TODO in TestSurfacesSeparateFromTheirBackground.
	unreachableAA := map[string]map[string]bool{
		"dark": {"error/errorSurface": true},
	}
	for _, test := range []struct {
		name    string
		palette palette
	}{
		{name: "dark", palette: darkPalette()},
		{name: "light", palette: lightPalette()},
	} {
		t.Run(test.name, func(t *testing.T) {
			for role, pair := range map[string][2]string{
				"error/errorSurface":   {test.palette.error, test.palette.errorSurface},
				"busy/busySurface":     {test.palette.busy, test.palette.busySurface},
				"codeText/codeSurface": {test.palette.codeText, test.palette.codeSurface},
			} {
				if unreachableAA[test.name][role] {
					continue
				}
				if got := contrastRatio(pair[0], pair[1]); got < 4.5 {
					t.Errorf("%s contrast = %.2f:1, want at least 4.5:1", role, got)
				}
			}
		})
	}
}

func contrastRatio(foreground, background string) float64 {
	foregroundLuminance := relativeLuminance(foreground)
	backgroundLuminance := relativeLuminance(background)
	lighter, darker := foregroundLuminance, backgroundLuminance
	if darker > lighter {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

func relativeLuminance(value string) float64 {
	rgb, err := strconv.ParseUint(strings.TrimPrefix(value, "#"), 16, 32)
	if err != nil {
		panic(err)
	}
	channel := func(raw uint64) float64 {
		value := float64(raw) / 255
		if value <= 0.04045 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(rgb>>16) + 0.7152*channel((rgb>>8)&0xff) + 0.0722*channel(rgb&0xff)
}

func TestSharedComponentStylesDeriveFromSemanticTokens(t *testing.T) {
	for _, test := range []struct {
		name    string
		theme   Theme
		palette palette
	}{
		{name: "dark", theme: Default(true), palette: darkPalette()},
		{name: "light", theme: Default(false), palette: lightPalette()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, want := test.theme.Panel.Frame.GetBorderTopForeground(), lipgloss.Color(test.palette.borderSubtle); got != want {
				t.Errorf("panel border = %v, want token %v", got, want)
			}
			if got, want := test.theme.Selector.Selected.GetForeground(), lipgloss.Color(test.palette.interactive); got != want {
				t.Errorf("selector selected = %v, want token %v", got, want)
			}
			if got, want := test.theme.Feedback.Error.GetForeground(), lipgloss.Color(test.palette.feedbackError); got != want {
				t.Errorf("feedback error = %v, want token %v", got, want)
			}
			if got, want := test.theme.Feedback.Success.GetForeground(), lipgloss.Color(test.palette.feedbackSuccess); got != want {
				t.Errorf("feedback success = %v, want token %v", got, want)
			}
			if got, want := test.theme.TextInput.Cursor.Color, lipgloss.Color(test.palette.interactive); got != want {
				t.Errorf("text cursor = %v, want token %v", got, want)
			}
			if got, want := test.theme.Chat.ToolName.GetForeground(), lipgloss.Color(test.palette.interactive); got != want {
				t.Errorf("tool action = %v, want token %v", got, want)
			}
			if got, want := test.theme.Chat.ToolArgument.GetForeground(), lipgloss.Color(test.palette.text); got != want {
				t.Errorf("tool argument = %v, want token %v", got, want)
			}
			if got, want := test.theme.Chat.ToolError.GetForeground(), lipgloss.Color(test.palette.feedbackError); got != want {
				t.Errorf("tool error = %v, want token %v", got, want)
			}
		})
	}
}

// TestSuccessStyleHasNoSurface follows the chip removal: a succeeded tool
// renders as a bare glyph, so StatusSuccess carries no background and the
// palette has no success surface role to go stale.
func TestSuccessStyleHasNoSurface(t *testing.T) {
	for _, test := range []struct {
		name   string
		isDark bool
	}{{"dark", true}, {"light", false}} {
		t.Run(test.name, func(t *testing.T) {
			bg := Default(test.isDark).Chat.StatusSuccess.GetBackground()
			if _, ok := bg.(lipgloss.NoColor); !ok {
				t.Errorf("StatusSuccess background = %v, want unset", bg)
			}
		})
	}
}

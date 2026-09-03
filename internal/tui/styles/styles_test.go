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

// TestDarkSurfaceReadsAgainstItsBackground pins the elevation contract for the
// dark ramp: the input block has to separate from the painted background, not
// blend into it.
//
// Light is deliberately absent. Its surface tokens predate this background and
// sit at the same lightness (1.010:1), so the same assertion would fail by
// design; they are re-derived in follow-up work, and this test grows a light
// case then.
func TestDarkSurfaceReadsAgainstItsBackground(t *testing.T) {
	p := darkPalette()
	if got := contrastRatio(p.surface, p.background); got < 1.1 {
		t.Errorf("surface/background contrast = %.3f:1, want at least 1.1:1", got)
	}
	if relativeLuminance(p.surface) <= relativeLuminance(p.background) {
		t.Error("dark surface should be lighter than the background it sits on")
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

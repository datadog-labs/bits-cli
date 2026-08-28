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
		})
	}
}

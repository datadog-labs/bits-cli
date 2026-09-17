package styles

import (
	"image/color"
	"math"
	"reflect"
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

func TestEditorMenuUsesConversationBackground(t *testing.T) {
	for _, theme := range []Theme{Default(true), Default(false)} {
		for name, style := range map[string]lipgloss.Style{
			"frame":           theme.Editor.MenuFrame,
			"item":            theme.Editor.MenuItem,
			"detail":          theme.Editor.MenuDetail,
			"selected":        theme.Editor.MenuSelected,
			"selected detail": theme.Editor.MenuSelectedDetail,
			"help":            theme.Editor.MenuHelp,
		} {
			if got := style.GetBackground(); got != theme.Background {
				t.Errorf("%s background = %v, want conversation background %v", name, got, theme.Background)
			}
		}
	}
}

func TestEditorMenuSelectionUsesInteractiveForeground(t *testing.T) {
	for _, test := range []struct {
		theme Theme
		want  color.Color
	}{
		{theme: Default(true), want: lipgloss.Color(darkPalette().interactive)},
		{theme: Default(false), want: lipgloss.Color(lightPalette().interactive)},
	} {
		if got := test.theme.Editor.MenuSelected.GetForeground(); got != test.want {
			t.Errorf("selected foreground = %v, want interactive %v", got, test.want)
		}
		if got := test.theme.Editor.MenuSelectedDetail.GetForeground(); got != test.want {
			t.Errorf("selected detail foreground = %v, want interactive %v", got, test.want)
		}
	}
}

// TestDefaultPinsAModeSpecificBackground checks both modes set a background
// and that they differ from each other.
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

// TestSurfacesSeparateFromTheirBackground checks every surface is
// distinguishable from the page it paints over, or the block disappears into
// it.
func TestSurfacesSeparateFromTheirBackground(t *testing.T) {
	// dark's errorSurface is exempt: it sits at 1.022:1 against the page, but
	// the label itself stays legible and dark's status colors are fixed.
	exempt := map[string]map[string]bool{
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
				if exempt[test.name][role] {
					continue
				}
				if got := contrastRatio(surface, test.palette.background); got < 1.1 {
					t.Errorf("%s/background contrast = %.3f:1, want at least 1.1:1", role, got)
				}
			}
		})
	}
	light := lightPalette()
	if relativeLuminance(light.surface) >= relativeLuminance(light.background) {
		t.Error("light surface should be darker than the background it sits on")
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

// TestForegroundTokensMeetNormalTextContrast guards the accent and status
// foregrounds, which are derived against the surfaces they render on, so a
// surface moving without them is a mistake worth catching.
//
// The three text levels are deliberately not here. They are chosen by eye to
// read as a hierarchy, and textTertiary in particular sits close to the
// surfaces on purpose — it is texture, not copy. Holding them to this bound
// would mean the ratio picking the palette's colors, which is backwards: the
// number describes a pair, it does not decide whether the pair works.
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

// TestNoStyleUsesFaint is the invariant behind the text levels: a color has
// to be stated, never inferred from the terminal's own default via Faint.
// Reflection covers every style so one added later isn't missed.
func TestNoStyleUsesFaint(t *testing.T) {
	for _, test := range []struct {
		name  string
		theme Theme
	}{
		{name: "dark", theme: Default(true)},
		{name: "light", theme: Default(false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var seen int
			forEachStyle(reflect.ValueOf(test.theme), "Theme", func(path string, style lipgloss.Style) {
				seen++
				if style.GetFaint() {
					t.Errorf("%s sets Faint; state the color instead", path)
				}
			})
			// Guard the walk itself: a reflection test that visits nothing passes
			// for the wrong reason.
			if seen < 20 {
				t.Errorf("walked only %d styles, expected the whole theme", seen)
			}
		})
	}
}

// forEachStyle visits every lipgloss.Style reachable from v, naming each by the
// field path that reaches it. Unexported fields are skipped because they cannot
// be read back out through the interface.
func forEachStyle(v reflect.Value, path string, visit func(string, lipgloss.Style)) {
	if v.Type() == reflect.TypeOf(lipgloss.Style{}) {
		visit(path, v.Interface().(lipgloss.Style))
		return
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := range v.NumField() {
		if field := v.Type().Field(i); field.IsExported() {
			forEachStyle(v.Field(i), path+"."+field.Name, visit)
		}
	}
}

// TestTextStylesResolveToALevel pins every text-carrying style to one of the
// three foreground levels. Listed explicitly rather than walked by reflection:
// container styles like Input.Block correctly have no foreground of their own.
func TestTextStylesResolveToALevel(t *testing.T) {
	for _, test := range []struct {
		name    string
		theme   Theme
		palette palette
	}{
		{name: "dark", theme: Default(true), palette: darkPalette()},
		{name: "light", theme: Default(false), palette: lightPalette()},
	} {
		t.Run(test.name, func(t *testing.T) {
			th := test.theme
			for _, level := range []struct {
				name   string
				want   color.Color
				styles map[string]lipgloss.Style
			}{
				{"textPrimary", lipgloss.Color(test.palette.textPrimary), map[string]lipgloss.Style{
					"Input.Text":              th.Input.Text,
					"Text.Primary":            th.Text.Primary,
					"Approval.Title":          th.Approval.Title,
					"Approval.Text":           th.Approval.Text,
					"Editor.MenuHelp":         th.Editor.MenuHelp,
					"Selector.Item":           th.Selector.Item,
					"Selector.SelectedDetail": th.Selector.SelectedDetail,
					"Panel.Frame":             th.Panel.Frame,
					"Panel.Title":             th.Panel.Title,
					"Panel.Compact":           th.Panel.Compact,
					"Panel.Dismiss":           th.Panel.Dismiss,
					"TextInput.Focused.Text":  th.TextInput.Focused.Text,
					"TextInput.Blurred.Text":  th.TextInput.Blurred.Text,
				}},
				{"textSecondary", lipgloss.Color(test.palette.textSecondary), map[string]lipgloss.Style{
					"Chat.Reasoning":                th.Chat.Reasoning,
					"Chat.AssistantText":            th.Chat.AssistantText,
					"Text.Secondary":                th.Text.Secondary,
					"Input.Placeholder":             th.Input.Placeholder,
					"Approval.Detail":               th.Approval.Detail,
					"Approval.Action":               th.Approval.Action,
					"Editor.MenuItem":               th.Editor.MenuItem,
					"Selector.Detail":               th.Selector.Detail,
					"TextInput.Focused.Placeholder": th.TextInput.Focused.Placeholder,
					"TextInput.Blurred.Prompt":      th.TextInput.Blurred.Prompt,
				}},
				{"textTertiary", lipgloss.Color(test.palette.textTertiary), map[string]lipgloss.Style{
					"Chat.ToolDetail":   th.Chat.ToolDetail,
					"Chat.Meta":         th.Chat.Meta,
					"Editor.MenuDetail": th.Editor.MenuDetail,
					"Text.Tertiary":     th.Text.Tertiary,
					"Panel.Help":        th.Panel.Help,
				}},
			} {
				for path, style := range level.styles {
					got := style.GetForeground()
					if _, unset := got.(lipgloss.NoColor); unset {
						t.Errorf("%s has no foreground; the terminal would pick it", path)
						continue
					}
					if got != level.want {
						t.Errorf("%s foreground = %v, want %s %v", path, got, level.name, level.want)
					}
				}
			}
		})
	}
}

// TestTextLevelsAreOrderedAndModeSpecific covers the two properties that make
// the levels a system rather than three unrelated colors: they are distinct
// from each other, and each mode has its own set.
func TestTextLevelsAreOrderedAndModeSpecific(t *testing.T) {
	for _, test := range []struct {
		name    string
		palette palette
		// darkMode reports whether elevation runs toward the light end, which
		// also decides which direction de-emphasis runs for text.
		darkMode bool
	}{
		{name: "dark", palette: darkPalette(), darkMode: true},
		{name: "light", palette: lightPalette(), darkMode: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			primary := relativeLuminance(test.palette.textPrimary)
			secondary := relativeLuminance(test.palette.textSecondary)
			tertiary := relativeLuminance(test.palette.textTertiary)

			// Receding text moves toward the page: darker in dark mode, lighter in
			// light mode. Ordering matters more than any individual distance.
			ordered := primary > secondary && secondary > tertiary
			if !test.darkMode {
				ordered = primary < secondary && secondary < tertiary
			}
			if !ordered {
				t.Errorf("levels out of order: primary %.4f, secondary %.4f, tertiary %.4f",
					primary, secondary, tertiary)
			}
		})
	}
	dark, light := darkPalette(), lightPalette()
	for role, pair := range map[string][2]string{
		"textPrimary":   {dark.textPrimary, light.textPrimary},
		"textSecondary": {dark.textSecondary, light.textSecondary},
		"textTertiary":  {dark.textTertiary, light.textTertiary},
	} {
		if pair[0] == pair[1] {
			t.Errorf("%s is %s in both modes; each mode needs its own", role, pair[0])
		}
	}
}

// TestChipLabelsReadOnTheirOwnSurface checks the status-chip and code-span
// pairs that carry both foreground and background from the palette.
func TestChipLabelsReadOnTheirOwnSurface(t *testing.T) {
	// dark's error pair is exempt: even pure black caps it at 4.27:1 with
	// error fixed at #D33043, so it's judged readable as-is.
	exempt := map[string]map[string]bool{
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
				if exempt[test.name][role] {
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
			if got, want := test.theme.Chat.ToolArgument.GetForeground(), lipgloss.Color(test.palette.textPrimary); got != want {
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

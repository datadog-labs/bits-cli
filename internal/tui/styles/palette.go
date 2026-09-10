package styles

// palette describes reusable visual roles. Component styles are built from it
// in builder.go, keeping colors centralized without tying the palette to a
// particular component.
type palette struct {
	primary     string
	interactive string
	secondary   string

	onAccent  string
	onWarning string

	// The text levels are the foreground ramp, ordered by attention: primary is
	// what the reader came for (input, titles, list items), secondary is still
	// meant to be read (reasoning, help, placeholders), tertiary is texture
	// (tool detail, labels, timestamps). Chosen by eye, not held to a contrast
	// bound.
	textPrimary   string
	textSecondary string
	textTertiary  string

	// background is painted across the alt-screen instead of inherited from the
	// terminal, and is the base of the elevation ramp surface* sits above.
	background string

	surface         string
	surfaceRaised   string
	approvalSurface string
	inputRule       string
	borderSubtle    string
	codeSurface     string
	codeText        string

	// sweepHot is the peak color of the composer's animated border sweep
	// (styles.BorderSweepRow). Dedicated rather than reusing primary, the
	// same way busyHot is dedicated rather than reusing it for the status
	// chip's shimmer — it lets this specific animation be tuned on its own.
	sweepHot string

	link string

	// busy roles style the in-flight status chip; busyDim/busyHot are the
	// low/high-emphasis ends of the sweep animated across its label.
	busy        string
	busySurface string
	busyDim     string
	busyHot     string

	success         string
	successSurface  string
	error           string
	errorSurface    string
	feedbackSuccess string
	feedbackError   string
	critical        string
	info            string
	warning         string
}

func darkPalette() palette {
	return palette{
		primary:         "#5e6dd6",
		interactive:     "#8B80F9",
		secondary:       "#3f4ca5",
		onAccent:        "#FFFFFF",
		onWarning:       "#1A1A1A",
		textPrimary:     "#FFFFFF",
		textSecondary:   "#A2A3A6",
		textTertiary:    "#45474D",
		background:      "#171921",
		surface:         "#22252F",
		surfaceRaised:   "236",
		approvalSurface: "#2C3142",
		inputRule:       "#383A40",
		borderSubtle:    "#474A54",
		codeSurface:     "#343336",
		codeText:        "#CECECE",
		sweepHot:        "#A2C6FF",
		link:            "#3d8bd0",
		busy:            "#F5C453",
		busySurface:     "#2E2409",
		busyDim:         "#8A6A1F",
		busyHot:         "#FFE9A8",
		success:         "#349C50",
		successSurface:  "#0A2F10",
		error:           "#D33043",
		errorSurface:    "#2F0A0F",
		feedbackSuccess: "#65A875",
		feedbackError:   "#D77480",
		critical:        "#C4314B",
		info:            "#632CA6",
		warning:         "#F5A623",
	}
}

// background (darker = raised, as in Nord/Solarized), since there is no
// headroom above a 94%-lightness background. Foregrounds are darkened to
// match, each checked against the darkest surface it renders on (builder.go).
// The three text levels are picked by eye; see the palette struct.
func lightPalette() palette {
	return palette{
		primary:         "#5e6dd6",
		interactive:     "#3F4991",
		secondary:       "#1d2140",
		onAccent:        "#FFFFFF",
		onWarning:       "#1A1A1A",
		textPrimary:     "#000000",
		textSecondary:   "#5E5F62",
		textTertiary:    "#989BA0",
		background:      "#ECEFF4",
		surface:         "#CCD3DF",
		surfaceRaised:   "#BDC5D3",
		approvalSurface: "#B7C1D5",
		inputRule:       "#383A40",
		borderSubtle:    "#9AA3B2",
		codeSurface:     "#CED3DD",
		codeText:        "#1C2E38",
		sweepHot:        "#A2C6FF",
		link:            "#006bc2",
		busy:            "#7A5200",
		busySurface:     "#E5D198",
		busyDim:         "#D2A340",
		busyHot:         "#4A3000",
		success:         "#41C464",
		successSurface:  "#EAFDED",
		error:           "#B61225",
		errorSurface:    "#F9C6CC",
		feedbackSuccess: "#30663E",
		feedbackError:   "#A13443",
		critical:        "#C4314B",
		info:            "#632CA6",
		warning:         "#F5A623",
	}
}

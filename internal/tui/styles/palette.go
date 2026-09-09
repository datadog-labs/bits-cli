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
	text      string
	muted     string

	// background is the app's own terminal background, painted across the whole
	// alt-screen rather than inherited from whatever the user's terminal is set
	// to. Pinning it means every other surface is composed against a known
	// color, so the UI looks the same in a Solarized terminal as in a default
	// one. It is the base of the elevation ramp: surface and the other surface*
	// roles sit above it, away from it in lightness.
	background string

	surface         string
	surfaceRaised   string
	approvalSurface string
	inputRule       string
	borderSubtle    string
	codeSurface     string
	codeText        string

	link string

	// busy roles style the in-flight status chip. busyDim and busyHot are the
	// low- and high-emphasis ends of the sweep animated across its label, so in
	// a light theme busyHot is the *darker* of the two.
	//
	// Open question for review: busy was previously raw ANSI ("11" dark / "3"
	// light) and is now hex, matching success and error. A gradient needs real
	// colors to interpolate between, so the sweep could not be built on the ANSI
	// pair — but this does change the flat chip's color on 16-color terminals,
	// which is a palette decision beyond the animation itself.
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
		text:            "252",
		muted:           "#8C8F99",
		background:      "#171921",
		surface:         "#22252F",
		surfaceRaised:   "236",
		approvalSurface: "#2C3142",
		inputRule:       "#383A40",
		borderSubtle:    "#474A54",
		codeSurface:     "#343336",
		codeText:        "#CECECE",
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

// lightPalette is the light-terminal palette.
//
// Its surfaces are derived from background rather than picked against an
// unknown terminal: each takes the background's hue and steps down in lightness
// until it reaches its elevation target. Light elevates downward — a raised
// surface is darker than the page, as in Nord and Solarized — because there is
// no headroom above a 94%-lightness background.
//
// The targets are twice the separation the ramp originally carried: contrast
// ratios near 1.0 mean "indistinguishable", so a role's useful signal is its
// distance above 1.0, and each role doubles that distance rather than its ratio.
// surface goes 1.150 -> 1.306, approvalSurface 1.279 -> 1.570, borderSubtle
// 1.603 -> 2.207. A ratio-doubling reading would have put surface at 2.30:1,
// which is dark-mode-inverted rather than a light theme with legible blocks.
//
// The foregrounds that ride on those surfaces are darkened to match. That is
// not cosmetic: surfaces moved further from the page, which moves them toward
// the text, and muted/interactive/error/feedback* would otherwise fall under
// 4.5:1. Each is set against the darkest surface it actually renders on — see
// builder.go for which those are — so it clears AA everywhere lighter too.
// See TestForegroundTokensMeetNormalTextContrast.
func lightPalette() palette {
	return palette{
		primary:         "#5e6dd6",
		interactive:     "#3F4991",
		secondary:       "#1d2140",
		onAccent:        "#FFFFFF",
		onWarning:       "#1A1A1A",
		text:            "#1C2E38",
		muted:           "#4F4F4F",
		background:      "#ECEFF4",
		surface:         "#CCD3DF",
		surfaceRaised:   "#BDC5D3",
		approvalSurface: "#B7C1D5",
		inputRule:       "#383A40",
		borderSubtle:    "#9AA3B2",
		codeSurface:     "#CED3DD",
		codeText:        "#1C2E38",
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

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

	// background is painted across the alt-screen instead of inherited from the
	// terminal, and is the base of the elevation ramp surface* sits above.
	background string

	surface         string
	surfaceRaised   string
	approvalSurface string
	inputRule       string
	inputText       string
	borderSubtle    string
	codeSurface     string
	codeText        string

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
		text:            "252",
		muted:           "#8C8F99",
		background:      "#171921",
		surface:         "#22252F",
		surfaceRaised:   "236",
		approvalSurface: "#2C3142",
		inputRule:       "#383A40",
		inputText:       "#FFFFFF",
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

// lightPalette is the light-terminal palette. Surfaces elevate downward from
// background (darker = raised, as in Nord/Solarized), since there is no
// headroom above a 94%-lightness background. Foregrounds are darkened to
// match, each checked against the darkest surface it renders on (builder.go).
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
		inputText:       "#000000",
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

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

func lightPalette() palette {
	return palette{
		primary:         "#5e6dd6",
		interactive:     "#4D58AF",
		secondary:       "#1d2140",
		onAccent:        "#FFFFFF",
		onWarning:       "#1A1A1A",
		text:            "#1C2E38",
		muted:           "#666666",
		surface:         "#EEF0F3",
		surfaceRaised:   "#EEF0F3",
		approvalSurface: "#E2E6EF",
		inputRule:       "#383A40",
		borderSubtle:    "#B8BCC4",
		codeSurface:     "#EEEFF0",
		codeText:        "#1C2E38",
		link:            "#006bc2",
		busy:            "#7A5200",
		busySurface:     "#FFF3D0",
		busyDim:         "#D9B15C",
		busyHot:         "#4A3000",
		success:         "#41C464",
		successSurface:  "#EAFDED",
		error:           "#EB364B",
		errorSurface:    "#FDEBED",
		feedbackSuccess: "#397A4A",
		feedbackError:   "#B23A4A",
		critical:        "#C4314B",
		info:            "#632CA6",
		warning:         "#F5A623",
	}
}

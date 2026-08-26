package styles

// palette describes reusable visual roles. Component styles are built from it
// in builder.go, keeping colors centralized without tying the palette to a
// particular component.
type palette struct {
	primary   string
	secondary string

	onAccent  string
	onWarning string
	text      string
	muted     string

	surface       string
	surfaceRaised string
	inputRule     string
	codeSurface   string
	codeText      string

	link string

	busy           string
	success        string
	successSurface string
	error          string
	errorSurface   string
	critical       string
	info           string
	warning        string
}

func darkPalette() palette {
	return palette{
		primary:        "#5e6dd6",
		secondary:      "#3f4ca5",
		onAccent:       "#FFFFFF",
		onWarning:      "#1A1A1A",
		text:           "252",
		muted:          "#666666",
		surface:        "#22252F",
		surfaceRaised:  "236",
		inputRule:      "#383A40",
		codeSurface:    "#343336",
		codeText:       "#CECECE",
		link:           "#3d8bd0",
		busy:           "11",
		success:        "#349C50",
		successSurface: "#0D2714",
		error:          "#D33043",
		errorSurface:   "#2F0A0F",
		critical:       "#C4314B",
		info:           "#632CA6",
		warning:        "#F5A623",
	}
}

func lightPalette() palette {
	return palette{
		primary:        "#5e6dd6",
		secondary:      "#1d2140",
		onAccent:       "#FFFFFF",
		onWarning:      "#1A1A1A",
		text:           "#1C2E38",
		muted:          "#666666",
		surface:        "#EEF0F3",
		surfaceRaised:  "#EEF0F3",
		inputRule:      "#383A40",
		codeSurface:    "#EEEFF0",
		codeText:       "#1C2E38",
		link:           "#006bc2",
		busy:           "3",
		success:        "#41C464",
		successSurface: "#ECF9EF",
		error:          "#EB364B",
		errorSurface:   "#FDEBED",
		critical:       "#C4314B",
		info:           "#632CA6",
		warning:        "#F5A623",
	}
}

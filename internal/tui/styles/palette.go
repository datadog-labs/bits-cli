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

	// accordionHover is the whole-row hover fill for the clickable disclosure
	// header. Dark gets a dedicated tone since surfaceRaised there is an
	// ANSI-256 index rather than a hex value that reads well as a fill. Light
	// started as a copy of surfaceRaised and was then lightened to 80% over the
	// page, the rows having read too heavy against it.
	accordionHover string

	// accordionDetailHover is accordionHover blended at 45% over the
	// background, used for a hovered block's rows below its header so the header
	// keeps the stronger tone.
	accordionDetailHover string

	// accordionBackground is the disclosure control's resting fill. Dark keeps
	// the hex it was fixed to by design request; light was later tuned off it,
	// so the two now differ like the rest of this file.
	accordionBackground string

	// sweepHot is the peak color of the composer's animated border sweep
	// (styles.BorderSweepRow). Dedicated rather than reusing primary, the
	// same way busyHot is dedicated rather than reusing it for the status
	// chip's shimmer — it lets this specific animation be tuned on its own.
	// Light's value currently coincides with primary; the field stays separate
	// so the two can be moved independently, as dark already does.
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

	// followBorder is the resting border of the jump-to-latest control, which
	// floats over the transcript. It is interactive at 40% over the page, so the
	// control reads as a quiet relative of the accent it turns into on hover
	// rather than as a generic panel edge: dark #8B80F9 over #171921 -> #454277,
	// light #5E6DD6 over #EDEFF6 -> #B4BBE9.
	followBorder string

	// selectionSurface and onSelection paint dragged-over cells. Selection used
	// to be the terminal's reverse-video attribute, which inverts the
	// terminal's own default colors rather than the theme's, so the result
	// drifted from the palette and read wrong once light mode's surfaces moved.
	// Each mode borrows the other's page and primary text, so a selection reads
	// as that row flipped into the opposite theme.
	selectionSurface string
	onSelection      string

	// fullAccessSurface is the focused fill for the approval modal's "Switch
	// to full access" choice. #EB364B does not exist in this palette (nearest
	// are critical #C4314B and error), so this is #EB364B at 50% over the
	// mode's background, pre-derived rather than reusing an existing token:
	// dark over #171921 -> #812836, light over #EDEFF6 -> #EC93A1.
	fullAccessSurface string
}

func darkPalette() palette {
	return palette{
		primary:              "#5e6dd6",
		interactive:          "#8B80F9",
		secondary:            "#3f4ca5",
		onAccent:             "#FFFFFF",
		onWarning:            "#1A1A1A",
		textPrimary:          "#FFFFFF",
		textSecondary:        "#A2A3A6",
		textTertiary:         "#45474D",
		background:           "#171921",
		surface:              "#22252F",
		surfaceRaised:        "236",
		approvalSurface:      "#2C3142",
		inputRule:            "#383A40",
		borderSubtle:         "#474A54",
		codeSurface:          "#343336",
		codeText:             "#CECECE",
		accordionHover:       "#22242B",
		accordionDetailHover: "#1C1E26",
		accordionBackground:  "#22242B",
		followBorder:         "#454277",
		selectionSurface:     "#EDEFF6",
		onSelection:          "#202F38",
		sweepHot:             "#A2C6FF",
		link:                 "#3d8bd0",
		busy:                 "#F5C453",
		busySurface:          "#2E2409",
		busyDim:              "#8A6A1F",
		busyHot:              "#FFE9A8",
		success:              "#349C50",
		successSurface:       "#0A2F10",
		error:                "#D33043",
		errorSurface:         "#2F0A0F",
		feedbackSuccess:      "#65A875",
		feedbackError:        "#D77480",
		critical:             "#C4314B",
		info:                 "#632CA6",
		warning:              "#F5A623",
		fullAccessSurface:    "#812836",
	}
}

// lightPalette turns its elevation ramp twice. The composer takes the headroom
// above the page by going near-white; everything stacked on the composer rises
// by darkening away from it (as in Nord/Solarized), since there is almost
// nothing left above. Foregrounds are darkened to match, each checked
// against the darkest surface it renders on (builder.go). The three text levels
// are picked by eye; see the palette struct.
func lightPalette() palette {
	return palette{
		primary:     "#5e6dd6",
		interactive: "#5E6DD6",
		secondary:   "#1d2140",
		onAccent:    "#FFFFFF",
		onWarning:   "#1A1A1A",
		// textPrimary is #1C2B34 at 98% over this palette's background.
		textPrimary:   "#202F38",
		textSecondary: "#737373",
		textTertiary:  "#989BA0",
		background:    "#EDEFF6",
		// surface is #FFFFFF at 90% over this palette's background.
		surface:              "#FDFDFE",
		surfaceRaised:        "#BDC5D3",
		approvalSurface:      "#B7C1D5",
		inputRule:            "#D2D2DA",
		borderSubtle:         "#9AA3B2",
		codeSurface:          "#E2E2E6",
		codeText:             "#1C2E38",
		accordionHover:       "#C7CDDA",
		accordionDetailHover: "#DCE0E9",
		accordionBackground:  "#E2E2E6",
		followBorder:         "#B4BBE9",
		selectionSurface:     "#171921",
		onSelection:          "#FFFFFF",
		sweepHot:             "#5E6DD6",
		link:                 "#006bc2",
		busy:                 "#7A5200",
		busySurface:          "#E5D198",
		busyDim:              "#D2A340",
		busyHot:              "#4A3000",
		success:              "#41C464",
		successSurface:       "#EAFDED",
		error:                "#B61225",
		errorSurface:         "#F9C6CC",
		feedbackSuccess:      "#30663E",
		feedbackError:        "#A13443",
		critical:             "#C4314B",
		info:                 "#632CA6",
		warning:              "#F5A623",
		fullAccessSurface:    "#EC93A1",
	}
}

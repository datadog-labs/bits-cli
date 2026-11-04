package tui

import (
	"image"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	exitCardTitle   = "Exit Bits?"
	exitCardDismiss = "ESC x"
	exitCardBody    = "Your conversation is saved. You can pick up where you left off anytime."
	exitCardHint    = "Enter"
	exitQuitLabel   = "Quit"
	exitKeepLabel   = "Keep working"
	exitButtonGap   = "  "
)

type exitChoice int

const (
	exitQuit exitChoice = iota
	exitKeepWorking
)

// exitCard is the "Exit Bits?" confirmation. The rectangles are screen cells,
// recorded on each render and empty while the card shows its compact form.
type exitCard struct {
	open     bool
	selected exitChoice

	quit, keep, dismiss image.Rectangle
}

func (m *Model) openExitCard() {
	m.exitCard = exitCard{open: true, selected: exitQuit}
}

func (m *Model) closeExitCard() {
	m.exitCard = exitCard{}
}

// exitButton renders a button; the highlighted one carries the Enter hint.
func exitButton(sty styles.ExitCard, label string, highlighted bool) string {
	if !highlighted {
		return sty.Action.Render(label)
	}
	inner := sty.Selected.UnsetPadding()
	return inner.PaddingLeft(1).Render(label+" ") +
		sty.Hint.UnsetPadding().Render(exitCardHint) +
		inner.PaddingRight(1).Render("")
}

func (m *Model) exitCardButtons() (quit, keep string) {
	sty := m.styles.ExitCard
	quit = exitButton(sty, exitQuitLabel, m.exitCard.selected == exitQuit)
	keep = exitButton(sty, exitKeepLabel, m.exitCard.selected == exitKeepWorking)
	return quit, keep
}

// exitCardOverlay centers the card over base, faded toward the background so
// the card is what the eye lands on. A terminal that cannot paint color
// cannot show the fade either, so it keeps the frame as drawn.
func (m *Model) exitCardOverlay(base string) string {
	sty := m.styles.ExitCard
	m.exitCard.quit, m.exitCard.keep, m.exitCard.dismiss = image.Rectangle{}, image.Rectangle{}, image.Rectangle{}
	if !m.monochrome {
		base = fade(base, m.width, m.height, sty.Text, m.styles.Background)
	}

	quit, keep := m.exitCardButtons()
	buttons := quit + exitButtonGap + keep
	bodyRows := 0
	content := components.PanelContent{
		Title:   exitCardTitle,
		Dismiss: exitCardDismiss,
		Body: func(width, _ int) string {
			body := sty.Body.Width(width).Render(exitCardBody)
			bodyRows = lipgloss.Height(body)
			return body + "\n\n" + buttons
		},
		MinBodyWidth:   lipgloss.Width(buttons),
		CompactTitle:   exitCardTitle,
		CompactMessage: exitCardBody,
	}
	panel := components.NewPanel(sty.Panel)
	card, _, bodyWidth := panel.Layout(m.width, m.height, content)
	cardWidth, cardHeight := lipgloss.Size(card)
	x, y := (m.width-cardWidth)/2, (m.height-cardHeight)/2

	if panel.Fits(m.width, m.height, content) {
		frame := sty.Panel.Frame
		bodyX := x + frame.GetBorderLeftSize() + frame.GetPaddingLeft()
		headerY := y + frame.GetBorderTopSize() + frame.GetPaddingTop()
		// Rows between the header and the body mirror the panel's section gap.
		buttonsY := headerY + 1 + max(0, sty.Panel.SectionGap) + bodyRows + 1
		quitX := bodyX
		keepX := quitX + lipgloss.Width(quit) + len(exitButtonGap)
		dismissX := bodyX + bodyWidth - ansi.StringWidth(exitCardDismiss)
		m.exitCard.quit = image.Rect(quitX, buttonsY, quitX+lipgloss.Width(quit), buttonsY+1)
		m.exitCard.keep = image.Rect(keepX, buttonsY, keepX+lipgloss.Width(keep), buttonsY+1)
		m.exitCard.dismiss = image.Rect(dismissX, headerY, dismissX+ansi.StringWidth(exitCardDismiss), headerY+1)
	}

	return lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(card).X(x).Y(y).Z(1),
	).Render()
}

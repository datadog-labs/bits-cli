package tui

import (
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	exitCardTitle   = "Exit Bits?"
	exitCardDismiss = "ESC x"
	exitCardBody    = "Your conversation is saved. You can pick up where you left off anytime."
	exitQuitLabel   = "Quit"
	exitKeepLabel   = "Keep working"
	exitButtonGap   = "  "

	exitCardCompactQuitHint = "enter quit · esc keep working"
	exitCardCompactKeepHint = "enter/esc keep working"
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

// openExitCard also drops pointer state: the card swallows motion and
// releases, so whatever the pointer last touched would stay highlighted
// (or half-pressed) behind the fade.
func (m *Model) openExitCard() {
	m.selection.clear()
	m.list.SetPointer(-1, -1)
	m.editor.SetPointer(-1, -1)
	m.editor.SetQueuePointer(-1, -1)
	m.follow.hover = false
	m.follow.pressed = image.Rectangle{}
	m.exitCard = exitCard{open: true, selected: exitQuit}
}

func (m *Model) closeExitCard() {
	m.exitCard = exitCard{}
}

// updateExitCard routes input while the card owns it. Typing and pasting
// hand the card's job back to the composer: the card closes, the editor
// regains focus ahead of the usual end-of-Update reconcile, and the message
// reaches it as if the card had never been there. A prompt waiting behind the
// card gets nothing: a reflex key must not answer it unseen.
func (m *Model) updateExitCard(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "enter":
			return m.activateExitChoice(m.exitCard.selected)
		case "esc":
			m.closeExitCard()
		case "left", "right", "tab", "shift+tab":
			m.exitCard.selected = otherExitChoice(m.exitCard.selected)
		default:
			if msg.Text != "" {
				return m.closeExitCardInto(msg)
			}
		}
	case tea.PasteMsg:
		return m.closeExitCardInto(msg)
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			return m.clickExitCard(pointAt(msg))
		}
	}
	return nil
}

func otherExitChoice(choice exitChoice) exitChoice {
	if choice == exitQuit {
		return exitKeepWorking
	}
	return exitQuit
}

func (m *Model) activateExitChoice(choice exitChoice) tea.Cmd {
	if choice == exitQuit {
		_, cmd := m.quit()
		return cmd
	}
	m.closeExitCard()
	return nil
}

func (m *Model) clickExitCard(at image.Point) tea.Cmd {
	switch {
	case at.In(m.exitCard.quit):
		return m.activateExitChoice(exitQuit)
	case at.In(m.exitCard.keep), at.In(m.exitCard.dismiss):
		return m.activateExitChoice(exitKeepWorking)
	}
	return nil
}

func (m *Model) closeExitCardInto(msg tea.Msg) tea.Cmd {
	m.closeExitCard()
	if m.prompt() != nil {
		return nil
	}
	blink := m.editor.Focus()
	_, cmd := m.dispatch(msg)
	return tea.Batch(blink, cmd)
}

func exitButton(sty styles.ExitCard, label string, highlighted bool) string {
	if highlighted {
		return sty.Selected.Render(label)
	}
	return sty.Action.Render(label)
}

func (m *Model) exitCardButtons() (quit, keep string) {
	sty := m.styles.ExitCard
	quit = exitButton(sty, exitQuitLabel, m.exitCard.selected == exitQuit)
	keep = exitButton(sty, exitKeepLabel, m.exitCard.selected == exitKeepWorking)
	return quit, keep
}

// exitCardOverlay centers the card over base, faded toward the background. A
// monochrome terminal cannot show the fade, so it keeps the frame as drawn.
func (m *Model) exitCardOverlay(base string) string {
	sty := m.styles.ExitCard
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
		MinBodyWidth: lipgloss.Width(buttons),
		CompactTitle: exitCardTitle,
		CompactBody: func(width int) string {
			hint := exitCardCompactQuitHint
			if m.exitCard.selected == exitKeepWorking {
				hint = exitCardCompactKeepHint
			}
			return sty.Body.Width(width).Render(exitCardBody) + "\n\n" + sty.Panel.Help.Render(hint)
		},
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
		keepX := quitX + lipgloss.Width(quit) + lipgloss.Width(exitButtonGap)
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

func isExitWord(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "exit", "quit", ":q":
		return true
	}
	return false
}

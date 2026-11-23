package tui

import (
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const (
	exitCardTitle   = "Exit Bits?"
	exitCardDismiss = "ESC x"
	exitCardBody    = "Your conversation is saved. You can pick up where you left off anytime."
	exitQuitLabel   = "Quit"
	exitKeepLabel   = "Keep working"

	exitCardCompactQuitHint = "enter quit · esc keep working"
	exitCardCompactKeepHint = "enter/esc keep working"
)

// exitChoice indexes the card's buttons.
type exitChoice int

const (
	exitQuit exitChoice = iota
	exitKeepWorking
)

// exitCard is the "Exit Bits?" confirmation. Its buttons and dismiss label
// are placed in the card's cells on each layout, and not at all while it
// shows its compact form.
type exitCard struct {
	open    bool
	buttons components.ButtonRow
	dismiss image.Rectangle
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
	m.exitCard = exitCard{open: true}
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
		if msg.String() == "esc" {
			m.closeExitCard()
			return nil
		}
		pressed, used := m.exitCard.buttons.Update(msg)
		if pressed {
			return m.pressExitButton()
		}
		if !used && msg.Text != "" {
			return m.closeExitCardInto(msg)
		}
	case tea.PasteMsg:
		return m.closeExitCardInto(msg)
	case tea.MouseClickMsg:
		over, at := m.frame.at(pointAt(msg))
		if over != regionPopup || msg.Button != tea.MouseLeft {
			return nil
		}
		if at.In(m.exitCard.dismiss) {
			m.closeExitCard()
			return nil
		}
		if pressed, _ := m.exitCard.buttons.Update(atPoint(msg, at)); pressed {
			return m.pressExitButton()
		}
	}
	return nil
}

// pressExitButton quits or keeps working, as the selected button says.
func (m *Model) pressExitButton() tea.Cmd {
	if exitChoice(m.exitCard.buttons.Selected) == exitQuit {
		_, cmd := m.quit()
		return cmd
	}
	m.closeExitCard()
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

// layoutExitCard centers the card and records where its controls landed.
func (m *Model) layoutExitCard() (string, image.Rectangle) {
	sty := m.styles.ExitCard
	selected := exitChoice(m.exitCard.buttons.Selected)
	buttons, rects := components.JoinButtons([]string{
		exitQuit:        exitButton(sty, exitQuitLabel, selected == exitQuit),
		exitKeepWorking: exitButton(sty, exitKeepLabel, selected == exitKeepWorking),
	})
	content := components.PanelContent{
		Title:   exitCardTitle,
		Dismiss: exitCardDismiss,
		Body: func(width, _ int) string {
			return sty.Body.Width(width).Render(exitCardBody) + "\n\n" + buttons
		},
		MinBodyWidth: lipgloss.Width(buttons),
		CompactTitle: exitCardTitle,
		CompactBody: func(width int) string {
			hint := exitCardCompactQuitHint
			if selected == exitKeepWorking {
				hint = exitCardCompactKeepHint
			}
			return sty.Body.Width(width).Render(exitCardBody) + "\n\n" + sty.Panel.Help.Render(hint)
		},
	}
	panel := components.NewPanel(sty.Panel)
	layout := panel.Layout(m.width, m.height, content)
	// The compact form shows a key hint instead: its buttons take keys only.
	placed := []components.PlacedButton{{Index: int(exitQuit)}, {Index: int(exitKeepWorking)}}
	m.exitCard.dismiss = image.Rectangle{}
	if panel.Fits(m.width, m.height, content) {
		// The buttons are the body's last row.
		origin := image.Pt(layout.Body.Min.X, layout.Body.Max.Y-1)
		for n := range placed {
			placed[n].At = rects[n].Add(origin)
		}
		m.exitCard.dismiss = layout.Dismiss
	}
	m.exitCard.buttons.Place(placed, true)
	width, height := lipgloss.Size(layout.View)
	x, y := (m.width-width)/2, (m.height-height)/2
	return layout.View, image.Rect(x, y, x+width, y+height)
}

func isExitWord(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "exit", "quit", ":q":
		return true
	}
	return false
}

package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
)

// turboModes are the inference modes turboOptions offer, in their order.
// Standard is the server's default, so it sends no mode.
var turboModes = [...]string{"", assistant.InferenceModeFast}

var turboOptions = [...]components.Option{
	{Label: "Standard", Description: "Optimal balance of cost and speed"},
	{Label: "Turbo", Description: "Faster, same quality, costs more"},
}

func (m *Model) switchTurbo(argument string) tea.Cmd {
	if m.engine == nil {
		return m.postNotice(notice(chat.NoticeError, nil, "This session has no assistant to configure."))
	}
	switch argument {
	case "":
		m.openTurboPrompt()
		return nil
	case "on":
		return m.setInferenceMode(assistant.InferenceModeFast)
	case "off":
		return m.setInferenceMode("")
	default:
		return m.postNotice(notice(chat.NoticeError, nil, "Unknown turbo setting %q. Use on or off.", argument))
	}
}

// openTurboPrompt offers Standard and Turbo with the current one marked and
// highlighted. NOTE: any mode but Turbo reads as Standard, so choosing
// Standard also clears a deep mode set through BITS_ASSISTANT_INFERENCE_MODE.
func (m *Model) openTurboPrompt() {
	index := 0
	if m.engine.InferenceMode() == assistant.InferenceModeFast {
		index = 1
	}
	form := components.NewChoicePrompt("Turbo Mode", "Changes apply from the next turn.", turboOptions[:], index)
	form.SetCurrent(index)
	openCommandPrompt(m, form, func(choice int) tea.Cmd { return m.setInferenceMode(turboModes[choice]) })
}

// setInferenceMode sets the mode of later turns: a running turn keeps the one
// it started with.
func (m *Model) setInferenceMode(mode string) tea.Cmd {
	if mode == m.engine.InferenceMode() {
		return nil
	}
	if err := m.engine.SetInferenceMode(mode); err != nil {
		return m.postNotice(notice(chat.NoticeError, err, "Could not switch turbo mode."))
	}
	text := "Turbo mode off."
	if mode == assistant.InferenceModeFast {
		text = "Turbo mode on."
	}
	if m.op.kind == opTurn {
		text += " Applies from the next turn."
	}
	return m.postNotice(notice(chat.NoticeInfo, nil, "%s", text))
}

// turbo reports whether later turns request Turbo, as the engine last
// published: views read it on every frame.
func (m *Model) turbo() bool { return m.state.InferenceMode == assistant.InferenceModeFast }

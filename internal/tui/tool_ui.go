package tui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/components"
)

// toolUISession pairs a pending tool request with the prompt answering it.
type toolUISession struct {
	request *tools.Request
	prompt  components.Prompt
}

func (s *toolUISession) cancelled() bool { return s.request.Context().Err() != nil }

type toolUIOpenedMsg struct{ request *tools.Request }

func waitToolUI(ui *tools.UI) tea.Cmd {
	if ui == nil {
		return nil
	}
	return func() tea.Msg { return toolUIOpenedMsg{request: <-ui.Requests()} }
}

func (m *Model) activateToolUI(request *tools.Request) {
	// A tool may present before a requested stop reaches its context.
	if request.Context().Err() != nil || m.op.stop != stopNone {
		return
	}
	prompt, ok := chat.NewToolPrompt(request.Call)
	if !ok {
		request.Respond(nil, fmt.Errorf("no interactive UI for tool %q", request.Call.Name))
		return
	}
	prompt.SetStyles(m.styles)
	session := &toolUISession{request: request, prompt: prompt}
	if m.activeToolUI != nil {
		m.queuedToolUIs = append(m.queuedToolUIs, session)
		return
	}
	m.activeToolUI = session
	m.selection.clear()
	m.editor.CloseMenu()
}

// nextToolUI shows the earliest queued call in transcript order: tools of one
// batch run concurrently, so arrival order is up to the scheduler.
func (m *Model) nextToolUI() {
	m.activeToolUI = nil
	m.queuedToolUIs = slices.DeleteFunc(m.queuedToolUIs, func(s *toolUISession) bool { return s.cancelled() })
	if len(m.queuedToolUIs) > 0 {
		position := func(s *toolUISession) int {
			return slices.IndexFunc(m.transcript.Blocks, func(b agent.Block) bool { return b.ToolCallID() == s.request.Call.ID })
		}
		next := 0
		for i, session := range m.queuedToolUIs {
			if position(session) < position(m.queuedToolUIs[next]) {
				next = i
			}
		}
		m.activeToolUI = m.queuedToolUIs[next]
		m.queuedToolUIs = slices.Delete(m.queuedToolUIs, next, next+1)
	}
}

func (m *Model) clearToolUIs() {
	m.activeToolUI = nil
	m.queuedToolUIs = nil
}

func (m *Model) reconcileToolUI() {
	if m.activeToolUI != nil && m.activeToolUI.cancelled() {
		m.nextToolUI()
	}
}

// stopTools answers ctrl+x on a docked prompt: the engine stops the
// client-tool round, settling its approvals and tool UIs, or the whole
// operation when it cannot.
func (m *Model) stopTools() {
	if m.engine.StopTools() {
		m.op.stop = max(m.op.stop, stopTools)
		m.clearToolUIs()
	} else {
		m.cancelOperation()
	}
}

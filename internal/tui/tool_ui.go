package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
)

type toolUISession struct {
	component chat.ToolInteraction
	ctx       context.Context
	done      chan struct{}
}

// ToolUI carries interactive tool calls to the model's event loop.
type ToolUI struct {
	requests chan *toolUISession
}

func NewToolUI() *ToolUI {
	return &ToolUI{requests: make(chan *toolUISession)}
}

func (h *ToolUI) Interact(ctx context.Context, call agent.ToolCall) (agent.ToolResult, error) {
	component, ok := chat.NewToolInteraction(call)
	if !ok {
		return agent.ToolResult{}, fmt.Errorf("no interactive UI for tool %q", call.Name)
	}
	session := &toolUISession{component: component, ctx: ctx, done: make(chan struct{})}
	select {
	case h.requests <- session:
	case <-ctx.Done():
		return agent.ToolResult{}, ctx.Err()
	}
	select {
	case <-session.done:
		// The event loop no longer touches the component once done is closed.
		result, _ := component.Result()
		return result, nil
	case <-ctx.Done():
		return agent.ToolResult{}, ctx.Err()
	}
}

type toolUIOpenedMsg struct{ session *toolUISession }

func waitToolUI(host *ToolUI) tea.Cmd {
	if host == nil {
		return nil
	}
	return func() tea.Msg { return toolUIOpenedMsg{session: <-host.requests} }
}

func (m *Model) activateToolUI(session *toolUISession) {
	// A tool may present before a requested stop reaches its context.
	if session.ctx.Err() != nil || m.cancelRequested || m.stoppingTools {
		return
	}
	if m.activeToolUI != nil {
		m.queuedToolUIs = append(m.queuedToolUIs, session)
		return
	}
	m.activeToolUI = session
	m.clearSelection()
	m.editor.CloseMenu()
	m.layoutTranscript()
}

func (m *Model) nextToolUI() {
	m.activeToolUI = nil
	for len(m.queuedToolUIs) > 0 {
		session := m.queuedToolUIs[0]
		m.queuedToolUIs = m.queuedToolUIs[1:]
		if session.ctx.Err() == nil {
			m.activeToolUI = session
			break
		}
	}
	m.layoutTranscript()
}

func (m *Model) clearToolUIs() {
	m.activeToolUI = nil
	m.queuedToolUIs = nil
	m.layoutTranscript()
}

func (m *Model) reconcileToolUI() {
	if m.activeToolUI != nil && m.activeToolUI.ctx.Err() != nil {
		m.nextToolUI()
	}
}

func (m *Model) updateToolUI(msg tea.Msg) tea.Cmd {
	if m.activeToolUI == nil {
		return nil
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+x" {
			if m.engine.StopTools() {
				m.stoppingTools = true
				m.clearToolUIs()
			} else {
				m.cancelRemote()
			}
			return nil
		}
	case tea.MouseClickMsg:
		msg.Y -= m.toolUITop()
		return m.forwardToolUI(msg)
	case tea.MouseWheelMsg:
		msg.Y -= m.toolUITop()
		return m.forwardToolUI(msg)
	}
	return m.forwardToolUI(msg)
}

func (m *Model) forwardToolUI(msg tea.Msg) tea.Cmd {
	cmd := m.activeToolUI.component.Update(msg)
	if _, done := m.activeToolUI.component.Result(); done {
		close(m.activeToolUI.done)
		m.nextToolUI()
	} else {
		m.layoutTranscript()
	}
	return cmd
}

func (m *Model) toolUITop() int {
	return m.list.Height() + chatNoticeHeight
}

func (m *Model) layoutToolUI() {
	if m.activeToolUI != nil {
		m.activeToolUI.component.SetSize(m.width, m.height, m.styles)
	}
}

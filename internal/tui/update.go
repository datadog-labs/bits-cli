package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/render"
)

// chrome rows reserved below the transcript viewport (status + input).
const chromeHeight = 2

// Update is the single message handler. Only this thread touches Model state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case turnEventMsg:
		m.applyEvent(msg.ev)
		m.refreshViewport()
		return m, waitEvent(m.turn)

	case turnClosedMsg:
		if m.phase != chat.PhaseError {
			m.phase = chat.PhaseIdle
		}
		m.turn = nil
		m.cancel = nil
		return m, nil
	}

	// Anything else (paste, etc.) goes to the input.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "esc":
		if m.cancel != nil {
			m.cancel() // interrupt the running turn
		}
		return m, nil
	case "enter":
		return m.submit()
	case "pgup", "pgdown", "ctrl+u", "ctrl+d":
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// submit starts a turn for the current input, unless it is empty or a turn is
// already running.
func (m *Model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" || m.turn != nil {
		return m, nil
	}
	m.input.SetValue("")
	m.transcript.AppendUser(text)

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.turn = m.engine.Start(ctx, text)
	m.phase = chat.PhaseWaiting
	m.errMsg = ""
	m.refreshViewport()
	return m, waitEvent(m.turn)
}

// applyEvent folds one engine event into the transcript / status. The switch is
// exhaustive over agent.EventKind.
//
//exhaustive:enforce
func (m *Model) applyEvent(ev agent.Event) {
	switch ev.Kind {
	case agent.EventDelta:
		m.phase = chat.PhaseStreaming
		m.transcript.AppendText(ev.ItemID, ev.Role, ev.Content, ev.Text)
	case agent.EventTool:
		m.transcript.UpsertTool(ev.ItemID, chat.ToolView{
			Name:   ev.Tool.Name,
			Input:  ev.Tool.Input,
			Output: ev.Tool.Output,
			Status: chat.ToolStatusOf(ev.Tool.Status),
		})
	case agent.EventUsage:
		m.usage = ev.Usage
	case agent.EventConversation:
		m.convID = ev.ConvID
	case agent.EventTurnDone:
		m.transcript.FinalizeAll()
		m.phase = chat.PhaseIdle
	case agent.EventError:
		m.transcript.FinalizeAll()
		m.phase = chat.PhaseError
		if ev.Err != nil {
			m.errMsg = ev.Err.Error()
		}
	case agent.EventNone:
		// nothing to do
	}
}

func (m *Model) resize(w, h int) {
	m.width, m.height = w, h
	vpHeight := max(1, h-chromeHeight)
	if !m.ready {
		m.viewport = viewport.New(w, vpHeight)
		m.viewport.MouseWheelEnabled = true
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = vpHeight
	}
	m.input.Width = max(1, w-len(m.input.Prompt)-1)
	m.refreshViewport()
}

// refreshViewport re-renders the transcript into the viewport, keeping the view
// pinned to the bottom while it was already there (auto-follow).
func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	pinned := m.viewport.AtBottom()
	m.viewport.SetContent(m.renderTranscript())
	if pinned {
		m.viewport.GotoBottom()
	}
}

func (m *Model) renderTranscript() string {
	items := m.transcript.Items()
	blocks := make([]string, len(items))
	for i := range items {
		blocks[i] = m.renderCached(items[i])
	}
	return strings.Join(blocks, "\n\n")
}

// renderCached memoizes each item's rendered output by version + width so only
// the changed (streaming) item re-renders.
func (m *Model) renderCached(it chat.Item) string {
	w := m.viewport.Width
	if e, ok := m.memo[it.ID]; ok && e.version == it.Version && e.width == w {
		return e.out
	}
	out := render.Item(it, w, m.styles)
	m.memo[it.ID] = memoEntry{version: it.Version, width: w, out: out}
	return out
}

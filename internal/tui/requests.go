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

// request is one tool call waiting on the user: an approval, or the
// interactive UI a client tool asks through. Each lives as long as its source
// says: an approval while the transcript lists it as awaiting a decision, a
// tool UI until it is answered or its tool's context ends.
type request struct {
	callID string
	prompt components.Prompt
	ui     *tools.Request // the tool's request; nil for an approval
	// respond hands the prompt's answer to whoever asked, and reports whether
	// it got through.
	respond  func(answer any) bool
	answered bool
}

type toolUIOpenedMsg struct{ request *tools.Request }

func waitToolUI(ui *tools.UI) tea.Cmd {
	if ui == nil {
		return nil
	}
	return func() tea.Msg { return toolUIOpenedMsg{request: <-ui.Requests()} }
}

// ask queues r behind the requests already waiting. The first one docks.
func (m *Model) ask(r *request) {
	r.prompt.SetStyles(m.styles)
	m.requests = append(m.requests, r)
	if len(m.requests) == 1 {
		m.editor.CloseMenu()
	}
}

// drop removes the requests gone tells it to. When the docked one goes, the
// earliest remaining call in transcript order docks next: tools of one batch
// run concurrently, so arrival order is up to the scheduler.
func (m *Model) drop(gone func(*request) bool) {
	wasDocked := len(m.requests) > 0 && gone(m.requests[0])
	m.requests = slices.DeleteFunc(m.requests, gone)
	if !wasDocked || len(m.requests) < 2 {
		return
	}
	position := func(r *request) int {
		return slices.IndexFunc(m.transcript.Blocks, func(b agent.Block) bool { return b.ToolCallID() == r.callID })
	}
	next := 0
	for i, r := range m.requests {
		if position(r) < position(m.requests[next]) {
			next = i
		}
	}
	docked := m.requests[next]
	m.requests = slices.Insert(slices.Delete(m.requests, next, next+1), 0, docked)
}

func (m *Model) openToolUI(ui *tools.Request) {
	// A tool may present before a requested stop reaches its context.
	if ui.Context().Err() != nil || m.op.stop != stopNone {
		return
	}
	prompt, ok := chat.NewToolPrompt(ui.Call)
	if !ok {
		ui.Respond(nil, fmt.Errorf("no interactive UI for tool %q", ui.Call.Name))
		return
	}
	m.ask(&request{callID: ui.Call.ID, prompt: prompt, ui: ui, respond: func(answer any) bool {
		ui.Respond(answer, nil)
		return true
	}})
}

// askApproval queues an approval of block's call.
func (m *Model) askApproval(block agent.Block) {
	id, prompt := block.ToolCallID(), newApprovalPrompt(block)
	// The prompt's own decision, typed, is the answer.
	m.ask(&request{callID: id, prompt: prompt, respond: func(any) bool {
		return m.engine.Decide(id, prompt.decision)
	}})
}

// updatePrompt applies msg to the docked prompt, then hands on its answer. An
// answered prompt gets no more input while it waits for its source to settle.
func (m *Model) updatePrompt(msg tea.Msg) (tea.Cmd, bool) {
	if len(m.requests) == 0 || m.requests[0].answered {
		return nil, false
	}
	r := m.requests[0]
	cmd, used := r.prompt.Update(msg)
	m.answer(r)
	return cmd, used
}

// answer hands r's answer, once given, to whoever asked; if it does not get
// through, the next update tries again. A tool UI is done once answered; an
// approval stays until the transcript drops its call.
func (m *Model) answer(r *request) {
	answer, done := r.prompt.Result()
	if !done {
		return
	}
	if r.answered = r.respond(answer); r.answered && r.ui != nil {
		m.drop(func(other *request) bool { return other == r })
	}
}

// syncRequests follows the transcript: approvals it no longer lists and tool
// UIs whose call was cancelled go, and new approvals queue in transcript
// order. A queued approval keeps its prompt, so a streaming update does not
// reset the highlight or the scroll.
func (m *Model) syncRequests(pending []agent.Block) {
	awaiting := make(map[string]bool, len(pending))
	for _, block := range pending {
		awaiting[block.ToolCallID()] = true
	}
	m.drop(func(r *request) bool {
		if r.ui != nil {
			return r.ui.Context().Err() != nil
		}
		return !awaiting[r.callID]
	})
	for _, block := range pending {
		if !slices.ContainsFunc(m.requests, func(r *request) bool { return r.ui == nil && r.callID == block.ToolCallID() }) {
			m.askApproval(block)
		}
	}
}

// dropToolUIs drops every tool UI once its round stops. Approvals stay until
// the transcript settles them.
func (m *Model) dropToolUIs() {
	m.drop(func(r *request) bool { return r.ui != nil })
}

// stopTools answers ctrl+x on a docked prompt: the engine stops the
// client-tool round, settling its approvals and tool UIs, or the whole
// operation when it cannot.
func (m *Model) stopTools() {
	if m.engine.StopTools() {
		m.op.stop = max(m.op.stop, stopTools)
		m.dropToolUIs()
	} else {
		m.cancelOperation()
	}
}

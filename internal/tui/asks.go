package tui

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui/chat"
	"github.com/DataDog/bits-cli/internal/tui/components"
)

// ask is one tool call waiting on the user, answered through its prompt. Two
// sources ask, each kept in its own shape: approvals mirror the transcript
// (m.approvals), tool UIs arrive as requests on the tools.UI channel
// (m.toolUIs). The chat shows one ask at a time (see reshow), placed where its
// prompt asks.
type ask interface {
	callID() string
	prompt() components.Prompt
	// deliver hands the prompt's answer, once given, to whoever asked. It
	// reports whether the answer got through; if not, it is tried again.
	deliver() bool
	// waiting reports whether the ask still needs the user: not yet answered,
	// and still held by its source.
	waiting() bool
}

// approvalAsk is one call the transcript lists as awaiting approval.
type approvalAsk struct {
	block    agent.Block
	panel    *approvalPrompt
	decide   func(callID string, decision agent.ApprovalDecision) bool
	answered bool // the decision reached the engine
	settled  bool // the transcript no longer lists the call
}

func (a *approvalAsk) callID() string            { return a.block.ToolCallID() }
func (a *approvalAsk) prompt() components.Prompt { return a.panel }
func (a *approvalAsk) waiting() bool             { return !a.answered && !a.settled }

func (a *approvalAsk) deliver() bool {
	if a.answered || a.panel.decision == "" {
		return false
	}
	a.answered = a.decide(a.callID(), a.panel.decision)
	return a.answered
}

// toolAsk is the interactive UI a client tool asks through.
type toolAsk struct {
	ui   *tools.Request
	form chat.ToolPrompt
	done bool // answered, or dropped with its round
}

func (t *toolAsk) callID() string            { return t.ui.Call.ID }
func (t *toolAsk) prompt() components.Prompt { return t.form }
func (t *toolAsk) waiting() bool             { return !t.done && t.ui.Context().Err() == nil }

func (t *toolAsk) deliver() bool {
	answer, answered := t.form.Result()
	if t.done || !answered {
		return false
	}
	t.ui.Respond(answer, nil)
	t.done = true
	return true
}

type toolUIOpenedMsg struct{ request *tools.Request }

func waitToolUI(ui *tools.UI) tea.Cmd {
	if ui == nil {
		return nil
	}
	return func() tea.Msg { return toolUIOpenedMsg{request: <-ui.Requests()} }
}

func (m *Model) openToolUI(ui *tools.Request) {
	// A tool may present before a requested stop reaches its context.
	if ui.Context().Err() != nil || m.op.stop != stopNone {
		return
	}
	form, ok := chat.NewToolPrompt(ui.Call)
	if !ok {
		ui.Respond(nil, fmt.Errorf("no interactive UI for tool %q", ui.Call.Name))
		return
	}
	form.SetStyles(m.styles)
	m.toolUIs = append(m.toolUIs, &toolAsk{ui: ui, form: form})
	m.editor.CloseMenu()
	m.reshow()
}

// syncApprovals mirrors the approvals the transcript lists: new calls are
// asked, calls it no longer lists are settled. An ask keeps its prompt while
// listed, so a streaming update resets neither the highlight nor the scroll,
// and an answered one is not asked again.
func (m *Model) syncApprovals(pending []agent.Block) {
	listed := make(map[string]bool, len(pending))
	for _, block := range pending {
		id := block.ToolCallID()
		listed[id] = true
		if m.approvals[id] != nil {
			continue
		}
		panel := newApprovalPrompt(block)
		panel.SetStyles(m.styles)
		m.approvals[id] = &approvalAsk{block: block, panel: panel, decide: func(id string, decision agent.ApprovalDecision) bool {
			return m.engine.Decide(id, decision)
		}}
		m.editor.CloseMenu()
	}
	maps.DeleteFunc(m.approvals, func(id string, a *approvalAsk) bool {
		a.settled = !listed[id]
		return a.settled
	})
	m.reshow()
}

// dropToolUIs lets go of every tool UI once its round stops, before their
// contexts end. Approvals follow the transcript.
func (m *Model) dropToolUIs() {
	for _, t := range m.toolUIs {
		t.done = true
	}
	m.toolUIs = nil
	m.reshow()
}

// clearAsks forgets every ask when the operation they belong to ends.
func (m *Model) clearAsks() {
	m.approvals = make(map[string]*approvalAsk)
	m.toolUIs, m.shown = nil, nil
}

// reshow keeps the shown ask while it waits; otherwise the earliest waiting
// call in the transcript is shown, whichever source asked. It also lets go of
// tool UIs that stopped waiting. Every change to the asks ends with it, and
// relayout runs it once per update to notice cancelled tool contexts.
func (m *Model) reshow() {
	m.toolUIs = slices.DeleteFunc(m.toolUIs, func(t *toolAsk) bool { return !t.waiting() })
	if m.shown != nil && m.shown.waiting() {
		return
	}
	m.shown = nil
	if waiting := m.waitingAsks(); len(waiting) > 0 {
		m.shown = slices.MinFunc(waiting, m.compareAsks)
	}
}

// waitingAsks returns the asks that still need the user, in no order.
func (m *Model) waitingAsks() []ask {
	asks := make([]ask, 0, len(m.approvals)+len(m.toolUIs))
	for _, a := range m.approvals {
		if a.waiting() {
			asks = append(asks, a)
		}
	}
	for _, t := range m.toolUIs {
		asks = append(asks, t)
	}
	return asks
}

// compareAsks orders asks by their call's place in the transcript. Tools of
// one batch run concurrently, so arrival order is up to the scheduler. Calls
// not in the transcript yet come last; the call ID breaks ties.
func (m *Model) compareAsks(a, b ask) int {
	position := func(x ask) int {
		if i := slices.IndexFunc(m.transcript.Blocks, func(block agent.Block) bool { return block.ToolCallID() == x.callID() }); i >= 0 {
			return i
		}
		return len(m.transcript.Blocks)
	}
	return cmp.Or(cmp.Compare(position(a), position(b)), cmp.Compare(a.callID(), b.callID()))
}

// updatePrompt applies msg to the shown prompt, then delivers its answer;
// once delivered, the next ask is shown.
func (m *Model) updatePrompt(msg tea.Msg) (tea.Cmd, bool) {
	if m.shown == nil {
		return nil, false
	}
	cmd, used := m.shown.prompt().Update(msg)
	if m.shown.deliver() {
		m.reshow()
	}
	return cmd, used
}

// stopTools answers ctrl+x on a shown prompt: the engine stops the
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

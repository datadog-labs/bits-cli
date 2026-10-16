package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/tui/chat"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
)

const approvalToolName = "confirm_action"

// newApprovalTool is a self-contained tool that always requires approval,
// used to drive the approval panel in these end-to-end tests.
func newApprovalTool() agent.Tool {
	return agent.Tool{
		Definition: assistant.ClientTool{
			Name:        approvalToolName,
			Description: "Test tool that requires approval before running.",
			InputSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			},
		},
		Approval: func(agent.ToolCall) (agent.ApprovalRequirement, bool) {
			return agent.ApprovalRequirement{
				Key: agent.ApprovalKey{Tool: approvalToolName, Resource: "test"},
				Prompt: agent.ApprovalPrompt{
					Title:  "Run the test action?",
					Detail: "This test tool requires approval before it runs.",
				},
			}, true
		},
		Handler: func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
			return agent.ToolResult{Title: "Done", Output: `{"status":"ok"}`}, nil
		},
	}
}

type approvalBackend struct {
	t          *testing.T
	responses  []assistant.ClientToolResponse
	calls      int
	serverGate bool
}

func (b *approvalBackend) Send(_ context.Context, message any, _ assistant.SendOptions, emit func(assistant.AssistantResponse) error) (string, error) {
	b.t.Helper()
	b.calls++
	if b.calls == 1 {
		content := assistant.ToolCallContent("tool-call", approvalToolName, `{}`)
		if b.serverGate {
			content = assistant.ToolCallContent("tool-call", assistant.ApprovalRequestTool, `{"tool_name":"delete_dashboard","tool_args":{"dashboard_id":"abc"},"tool_call_id":"tool-call","approval_message":"Delete it?"}`)
		}
		content.Type = assistant.ContentClientToolCall
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("tool-message", content)
		return "conversation-1", emit(response)
	}

	var ok bool
	b.responses, ok = message.([]assistant.ClientToolResponse)
	if !ok {
		b.t.Fatalf("tool follow-up has type %T", message)
	}
	for _, chunk := range []string{"Do", "ne."} {
		var response assistant.AssistantResponse
		response.Data.Attributes.StructuredMessage = assistant.AssistantMessage("answer", assistant.TextContent(chunk))
		if err := emit(response); err != nil {
			return "conversation-1", err
		}
	}
	return "conversation-1", nil
}

func TestApprovalDenialContinuesStreaming(t *testing.T) {
	for _, serverGate := range []bool{false, true} {
		for _, key := range []rune{tea.KeyEscape, tea.KeyEnter} {
			t.Run(fmt.Sprintf("server_gate_%t/key_%d", serverGate, key), func(t *testing.T) {
				backend := &approvalBackend{t: t, serverGate: serverGate}
				tool := newApprovalTool()
				tool.Handler = func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
					t.Error("denied tool executed")
					return agent.ToolResult{}, nil
				}
				tools, err := agent.NewToolSet(agent.ModeManual, tool)
				if err != nil {
					t.Fatal(err)
				}
				model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
				model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
				setConversationInput(model, "Run the action")
				_, _ = model.submit()
				for waitingApprovals(model) == 0 {
					msg := runConversationCmd(t, waitEvent(model.op.gen, model.op.events))
					_, _ = model.Update(msg)
				}

				if key == tea.KeyEnter {
					// Move past Allow, Allow for session, and Switch to full
					// access to select Deny.
					_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
					_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
					_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
				}
				_, _ = model.handleKey(tea.KeyPressMsg{Code: key})
				sawPartial := false
				for model.op.events != nil {
					msg := runConversationCmd(t, waitEvent(model.op.gen, model.op.events))
					_, _ = model.Update(msg)
					for _, block := range model.transcript.Blocks {
						if block.Markdown != nil && block.Markdown.Content == "Do" && !block.Complete {
							sawPartial = true
						}
					}
				}
				if backend.calls != 2 || len(backend.responses) != 1 || backend.responses[0].Status != assistant.ToolStatusError {
					t.Fatalf("denial not sent: calls=%d responses=%+v", backend.calls, backend.responses)
				}
				if !sawPartial {
					t.Error("model's follow-up response did not stream after denial")
				}
				if view := ansi.Strip(model.View().Content); !strings.Contains(view, "Done.") {
					t.Fatalf("model's follow-up answer missing after denial:\n%s", view)
				}
			})
		}
	}
}

func TestApprovalBlursEditorUntilResolved(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeManual, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
	model.Init() // focuses the editor, starting the cursor blink
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !model.editor.Focused() {
		t.Fatal("editor not focused after init")
	}

	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for waitingApprovals(model) == 0 {
		msg := runConversationCmd(t, waitEvent(model.op.gen, model.op.events))
		_, _ = model.Update(msg)
	}
	if model.editor.Focused() {
		t.Fatal("editor stayed focused while an approval owned the composer")
	}

	_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape}) // deny
	drainConversationRemote(t, model)
	if !model.editor.Focused() {
		t.Fatal("editor not refocused after the approval resolved")
	}
}

func TestConcealedApprovalIgnoresAllKeysUntilResized(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeManual, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for waitingApprovals(model) == 0 {
		msg := runConversationCmd(t, waitEvent(model.op.gen, model.op.events))
		_, _ = model.Update(msg)
	}

	// Shrink below the approval minimum: View hides the whole chat behind the
	// resize hint, so no keypress may drive the hidden prompt — not even Esc.
	model.Update(tea.WindowSizeMsg{Width: approvalMinWidth - 1, Height: 24})
	if !model.frame.tooSmall {
		t.Fatal("chat not concealed at the reduced size")
	}
	if view := ansi.Strip(model.View().Content); strings.Contains(view, "Permission Required") {
		t.Fatalf("concealed approval still rendered:\n%s", view)
	}

	for _, code := range []rune{tea.KeyRight, tea.KeyEnter, tea.KeyEscape} {
		_, _ = model.Update(tea.KeyPressMsg{Code: code})
	}
	if waitingApprovals(model) == 0 {
		t.Fatal("a concealed keypress answered the approval")
	}
	if backend.calls != 1 {
		t.Fatalf("backend calls = %d while concealed, want 1 (no follow-up)", backend.calls)
	}

	// Resizing back restores the prompt and its controls; Esc then denies.
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	_, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	drainConversationRemote(t, model)
	// The denial is answered on the wire and the follow-up answer renders.
	if backend.calls != 2 || len(backend.responses) != 1 {
		t.Fatalf("deny after resize not honored: calls=%d responses=%d", backend.calls, len(backend.responses))
	}
	if backend.responses[0].Status != assistant.ToolStatusError || backend.responses[0].ToolCallID != "tool-call" {
		t.Fatalf("denial response = %+v", backend.responses[0])
	}
	if view := ansi.Strip(model.View().Content); !strings.Contains(view, "Done.") {
		t.Fatalf("model's follow-up answer missing after denial:\n%s", view)
	}
}

func TestToolApprovalComposerSuppressedInSkipPermissions(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeSkipPermissions, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	drainConversationRemote(t, model)

	if waitingApprovals(model) != 0 {
		t.Fatalf("skip-permissions surfaced %d approval prompts", waitingApprovals(model))
	}
	view := ansi.Strip(model.View().Content)
	if strings.Contains(view, "Permission Required") || strings.Contains(view, "Run the test action?") {
		t.Fatalf("approval panel rendered in skip-permissions mode:\n%s", view)
	}
	if !model.editor.Focused() {
		t.Fatal("editor lost focus without an approval owning the composer")
	}
	if len(backend.responses) != 1 || !strings.Contains(backend.responses[0].Metadata.Output, `"status":"ok"`) {
		t.Fatalf("tool response = %+v, want the tool to have run unprompted", backend.responses)
	}
}

func TestApprovalPanelResponsiveLayout(t *testing.T) {
	// 36 is the minimum slot (shows only the choices that never drop,
	// condensed). 40 is below the all-four-condensed boundary, so "Allow for
	// session" still drops. 44 is that boundary: all four fit condensed. 70
	// is the full-label boundary that used to silently shear off "Deny" (the
	// full-label row is 61 cells; measurement now falls back to condensed
	// instead of truncating). 80 and 120 show every label in full.
	for _, width := range []int{36, 40, 44, 70, 80, 120} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			backend := &approvalBackend{t: t}
			tools, err := agent.NewToolSet(agent.ModeManual, newApprovalTool())
			if err != nil {
				t.Fatal(err)
			}
			model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
			model.Update(tea.WindowSizeMsg{Width: width, Height: 24})
			setConversationInput(model, "Run the action")
			_, _ = model.submit()
			for waitingApprovals(model) == 0 {
				msg := runConversationCmd(t, waitEvent(model.op.gen, model.op.events))
				_, _ = model.Update(msg)
			}

			view := model.frame.promptView
			plain := ansi.Strip(view)
			normalized := strings.Join(strings.Fields(plain), " ")
			for _, want := range []string{"Permission Required", "ESC x", "Run the test action?", "This test tool requires", "Allow", "Deny"} {
				if !strings.Contains(plain, want) {
					t.Errorf("approval panel missing %q:\n%s", want, plain)
				}
			}
			for lineNo, line := range strings.Split(view, "\n") {
				if got := ansi.StringWidth(line); got > width {
					t.Fatalf("line %d width = %d, want <= %d", lineNo+1, got, width)
				}
			}
			switch {
			case width < 44:
				// Narrow enough that "Allow for session" gives way first
				// (decision: never Allow, never Deny); "Session" and "Full"
				// (its condensed label) are gone with it.
				if !strings.Contains(plain, "Full") || strings.Contains(plain, "Session") || strings.Contains(plain, "Allow for session") {
					t.Fatalf("narrow actions did not drop Allow for session:\n%s", plain)
				}
			case width < 80:
				// All four choices fit, but only condensed.
				if !strings.Contains(normalized, "Allow Session Full Deny") {
					t.Fatalf("condensed actions missing:\n%s", plain)
				}
			default:
				if !strings.Contains(normalized, "Allow Allow for session Switch to full access Deny") {
					t.Fatalf("full actions missing:\n%s", plain)
				}
			}
			if width == 120 {
				firstLine := strings.Split(plain, "\n")[0]
				margin := model.styles.Approval.Panel.HorizontalMargin
				if got := strings.Index(firstLine, "╭"); got != margin {
					t.Fatalf("wide panel left offset = %d, want %d:\n%s", got, margin, plain)
				}
				if got, want := ansi.StringWidth(strings.TrimRight(firstLine, " ")), width-margin; got != want {
					t.Fatalf("wide panel right edge = %d, want %d:\n%s", got, want, plain)
				}
			}
		})
	}
}

func TestExecCommandApprovalPanelShowsFullMultilineCommand(t *testing.T) {
	command := "python3 - <<'PY'\nprint('first')\nprint('second')\nPY"
	input, err := json.Marshal(spec.ExecCommandInput{Cmd: command})
	if err != nil {
		t.Fatal(err)
	}
	model := newShell()
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model.syncApprovals([]agent.Block{{
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{
			Name:         spec.ExecCommand,
			Input:        string(input),
			Status:       agent.ToolAwaitingApproval,
			IsClientSide: true,
			Approval: &agent.ApprovalPrompt{
				Title:  "Run an unsandboxed command?",
				Detail: "cwd: /workspace · unsandboxed",
			},
		},
	}})

	model.relayout()
	plain := ansi.Strip(model.frame.promptView)
	wants := []string{"Run an unsandboxed command?", "cwd: /workspace · unsandboxed", "Allow"}
	wants = append(wants, strings.Split(command, "\n")...)
	for _, want := range wants {
		if !strings.Contains(plain, want) {
			t.Errorf("exec approval panel missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "␊") {
		t.Fatalf("exec approval panel exposed newline as a control picture:\n%s", plain)
	}
}

func TestExecCommandApprovalPanelPagesLongCommands(t *testing.T) {
	commandRows := make([]string, 20)
	for i := range commandRows {
		commandRows[i] = fmt.Sprintf("print(%d)", i+1)
	}
	input, err := json.Marshal(spec.ExecCommandInput{Cmd: strings.Join(commandRows, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	model := newShell()
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 22})
	model.syncApprovals([]agent.Block{{
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{
			Name:         spec.ExecCommand,
			Input:        string(input),
			Status:       agent.ToolAwaitingApproval,
			IsClientSide: true,
			Approval: &agent.ApprovalPrompt{
				Title:  "Run an unsandboxed command?",
				Detail: "cwd: /workspace · unsandboxed",
			},
		},
	}})

	model.relayout()
	first := ansi.Strip(model.frame.promptView)
	if !strings.Contains(first, "print(1)") || strings.Contains(first, "print(20)") || !strings.Contains(first, "· pgup/pgdown scroll") || !strings.Contains(first, "Allow") {
		t.Fatalf("initial long-command approval window is incorrect:\n%s", first)
	}
	for range 10 {
		model.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	last := ansi.Strip(model.frame.promptView)
	if strings.Contains(last, "print(1)") || !strings.Contains(last, "print(20)") || !strings.Contains(last, "cwd: /workspace") || !strings.Contains(last, "Allow") {
		t.Fatalf("paged long-command approval window is incorrect:\n%s", last)
	}
}

func TestToolApprovalComposerE2E(t *testing.T) {
	tests := []struct {
		name      string
		navigate  int
		deny      bool
		selection string
	}{
		{name: "allow", selection: "Allow"},
		{name: "allow for session", navigate: 1, selection: "Allow for session"},
		{name: "deny", navigate: 3, deny: true, selection: "Deny"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &approvalBackend{t: t}
			tools, err := agent.NewToolSet(agent.ModeManual, newApprovalTool())
			if err != nil {
				t.Fatal(err)
			}
			model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
			model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			setConversationInput(model, "Run the action")
			_, _ = model.submit()

			for waitingApprovals(model) == 0 {
				msg := runConversationCmd(t, waitEvent(model.op.gen, model.op.events))
				_, _ = model.Update(msg)
			}
			for range tt.navigate {
				model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			}

			approval := model.frame.promptView
			view := ansi.Strip(model.View().Content)
			// The chip renders its label as separate background segments
			// (see approvalChoice.chip), so compare against the choice's own
			// chip rendering rather than a plain Style.Render of the label.
			i := slices.IndexFunc(approvalChoices[:], func(c approvalChoice) bool { return c.label == tt.selection })
			if i < 0 {
				t.Fatalf("no approval choice labeled %q", tt.selection)
			}
			wantChip := approvalChoices[i].chip(model.styles.Approval.Selected, false)
			if !strings.Contains(view, "Permission Required") || !strings.Contains(view, "Run the test action?") || !strings.Contains(approval, wantChip) {
				t.Fatalf("approval panel not rendered:\n%s", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if width := ansi.StringWidth(line); width > 80 {
					t.Fatalf("approval view width = %d, want <= 80", width)
				}
			}
			_, _ = model.handleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
			if model.editor.Value() != "" {
				t.Fatal("approval input leaked into the editor")
			}

			_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			drainConversationRemote(t, model)

			if strings.Contains(ansi.Strip(model.View().Content), "Permission Required") {
				t.Fatal("approval panel remained after the decision")
			}
			if tt.deny {
				// The denial is answered on the wire and the follow-up answer renders.
				if backend.calls != 2 || len(backend.responses) != 1 {
					t.Fatalf("backend calls = %d responses = %d, want the denial answered on the wire", backend.calls, len(backend.responses))
				}
				if backend.responses[0].Status != assistant.ToolStatusError || backend.responses[0].ToolCallID != "tool-call" {
					t.Fatalf("denial response = %+v", backend.responses[0])
				}
				if !strings.Contains(ansi.Strip(model.View().Content), "Done.") {
					t.Fatal("model's follow-up answer missing after denial")
				}
				return
			}
			if len(backend.responses) != 1 || !strings.Contains(backend.responses[0].Metadata.Output, `"status":"ok"`) {
				t.Fatalf("tool response = %+v", backend.responses)
			}
		})
	}
}

func TestApprovalKeepsTranscriptScrollable(t *testing.T) {
	commandRows := make([]string, 60)
	for i := range commandRows {
		commandRows[i] = fmt.Sprintf("print(%d)", i+1)
	}
	input, err := json.Marshal(spec.ExecCommandInput{Cmd: strings.Join(commandRows, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	model := newShell()
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	for i := range 80 {
		model.notices = append(model.notices, chat.NoticeItem{ID: uint64(i + 1), Notice: chat.Notice{Level: chat.NoticeInfo, Text: fmt.Sprintf("line %d", i)}})
	}
	model.syncTranscript()
	model.syncApprovals([]agent.Block{{
		Kind: assistant.KindToolCall,
		Tool: &agent.ToolBlock{Name: spec.ExecCommand, Input: string(input), Status: agent.ToolAwaitingApproval, IsClientSide: true},
	}})
	model.relayout()

	if transcript, prompt := model.frame.transcript.Dy(), model.frame.prompt.Dy(); transcript < prompt {
		t.Fatalf("transcript rows = %d, want at least the approval's %d", transcript, prompt)
	}

	top := model.list.VisibleSurface().Top
	model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, Y: 0})
	if model.list.VisibleSurface().Top >= top {
		t.Fatal("wheel over the transcript did not scroll it while approval is pending")
	}
	top = model.list.VisibleSurface().Top
	model.Update(tea.KeyPressMsg{Code: tea.KeyPgUp, Mod: tea.ModShift})
	if model.list.VisibleSurface().Top >= top {
		t.Fatal("shift+pgup did not scroll the transcript while approval is pending")
	}

	// The wheel over the panel and pgup/pgdown scroll its body, not the chat.
	top = model.list.VisibleSurface().Top
	model.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, Y: model.frame.prompt.Min.Y})
	if !strings.Contains(ansi.Strip(model.frame.promptView), "lines 2–") {
		t.Fatalf("wheel did not scroll the approval body:\n%s", ansi.Strip(model.frame.promptView))
	}
	model.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if strings.Contains(ansi.Strip(model.frame.promptView), "lines 2–") {
		t.Fatalf("pgdown did not page the approval body:\n%s", ansi.Strip(model.frame.promptView))
	}
	if model.list.VisibleSurface().Top != top {
		t.Fatal("scrolling the approval panel moved the transcript")
	}

	// The panel has no use for clicks, so they select its text, e.g. to copy
	// the command.
	model.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 4, Y: model.frame.prompt.Min.Y + 1})
	if !model.selection.selecting() || model.selection.scope != selectionScopeLower {
		t.Fatal("a click on the approval did not start a selection of its text")
	}
}

// ctrl+x stops the tool round from any docked prompt, an approval included.
func TestCtrlXStopsTheToolsFromAnApproval(t *testing.T) {
	backend := &approvalBackend{t: t}
	tool := newApprovalTool()
	tool.Handler = func(context.Context, agent.ToolCall) (agent.ToolResult, error) {
		t.Error("stopped tool executed")
		return agent.ToolResult{}, nil
	}
	tools, err := agent.NewToolSet(agent.ModeManual, tool)
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for waitingApprovals(model) == 0 {
		_, _ = model.Update(runConversationCmd(t, waitEvent(model.op.gen, model.op.events)))
	}

	_, _ = model.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if model.op.stop != stopTools {
		t.Fatalf("stop = %d, want the tool round stopped", model.op.stop)
	}
	drainConversationRemote(t, model)
	if waitingApprovals(model) > 0 || model.focus() != focusEditor {
		t.Fatal("the approval outlived the stopped tool round")
	}
}

// awaitApproval drives model to the first approval prompt at width, with
// "Run the action" submitted.
func awaitApproval(t *testing.T, model *Model, width int) {
	t.Helper()
	model.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	setConversationInput(model, "Run the action")
	_, _ = model.submit()
	for waitingApprovals(model) == 0 {
		msg := runConversationCmd(t, waitEvent(model.op.gen, model.op.events))
		_, _ = model.Update(msg)
	}
}

// TestApprovalFullAccessShowsDisclaimerOnlyWhenFocused covers decision 2 (one
// row, never wrapped) and the revision after human testing of the built
// binary: the disclaimer row is reserved unconditionally — blank unless full
// access is focused — so the panel's rendered HEIGHT never changes with
// focus. Only the row's CONTENT does. That invariant is the actual fix the
// human asked for: before it, focusing full access grew the footer by a row
// and the whole modal visibly jumped.
//
// The disclaimer does not grow the panel at all, focused or not: it occupies
// a row the approval Frame gives up from its own bottom padding (see
// builder.go's approvalPanel), so the heights below are pinned to what the
// panel measured at 851a96c, before this feature existed.
func TestApprovalFullAccessShowsDisclaimerOnlyWhenFocused(t *testing.T) {
	baselineHeight := map[int]int{approvalMinWidth: 11, 44: 11, 80: 10, 120: 10}
	for _, width := range []int{approvalMinWidth, 44, 80, 120} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			backend := &approvalBackend{t: t}
			tools, err := agent.NewToolSet(agent.ModeManual, newApprovalTool())
			if err != nil {
				t.Fatal(err)
			}
			model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
			awaitApproval(t, model, width)

			plain := ansi.Strip(model.frame.promptView)
			if strings.Contains(plain, "Full access skips") {
				t.Fatalf("disclaimer shown before full access is focused:\n%s", plain)
			}
			unfocusedHeight := lipgloss.Height(model.frame.promptView)
			if unfocusedHeight != baselineHeight[width] {
				t.Fatalf("unfocused panel height = %d, want %d (the height at 851a96c, before full access existed)", unfocusedHeight, baselineHeight[width])
			}

			// Walk every choice; the disclaimer TEXT shows on full access
			// alone, but the panel's HEIGHT must equal unfocusedHeight on
			// every single one of them — the jump the human flagged.
			for range approvalChoices {
				model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
				plain := ansi.Strip(model.frame.promptView)
				focused := approvalChoices[model.approvals[model.shown.callID()].panel.selected]
				hasDisclaimer := strings.Contains(plain, "Full access skips")
				if focused.fullAccess() != hasDisclaimer {
					t.Fatalf("focused=%q fullAccess=%v disclaimer-shown=%v:\n%s", focused.label, focused.fullAccess(), hasDisclaimer, plain)
				}
				if got := lipgloss.Height(model.frame.promptView); got != unfocusedHeight {
					t.Fatalf("focused=%q panel height = %d, want %d (unfocused) unchanged:\n%s", focused.label, got, unfocusedHeight, plain)
				}
				if hasDisclaimer {
					lines := strings.Split(plain, "\n")
					count := 0
					for _, line := range lines {
						if strings.Contains(line, "Full access skips") {
							count++
							if got := ansi.StringWidth(line); got > width {
								t.Fatalf("disclaimer line width = %d, want <= %d:\n%s", got, width, plain)
							}
						}
					}
					if count != 1 {
						t.Fatalf("disclaimer rendered on %d rows, want exactly 1:\n%s", count, plain)
					}
				}
			}
		})
	}
}

// TestApprovalFocusCycleNeverLandsOnAHiddenChoice guards the safety bug the
// plan calls out explicitly: at a narrow width "Allow for session" is
// dropped from the row, so it must also leave the keyboard focus cycle.
// Cycling past the end more times than there are visible choices must never
// land the cursor on it.
func TestApprovalFocusCycleNeverLandsOnAHiddenChoice(t *testing.T) {
	backend := &approvalBackend{t: t}
	tools, err := agent.NewToolSet(agent.ModeManual, newApprovalTool())
	if err != nil {
		t.Fatal(err)
	}
	model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
	awaitApproval(t, model, approvalMinWidth)

	panel := model.approvals[model.shown.callID()].panel
	// Cycle past the end more times than there are visible choices at this
	// width (3, per the geometry in approval.go): a cycle with no notion of
	// visibility would eventually land on the hidden "Allow for session".
	for range 20 {
		_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
		if !slices.Contains(panel.visible, panel.selected) {
			t.Fatalf("focus landed on a hidden choice: selected=%d visible=%v", panel.selected, panel.visible)
		}
	}
	_, _ = model.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if panel.grant == grantSession {
		t.Fatal("enter on a hidden choice granted a session-wide allow")
	}
}

// TestApprovalFullAccessSwitchesAtOnce covers the modal's full access: the
// switch applies and is announced before the call is answered, mid-turn, and
// exactly once.
func TestApprovalFullAccessSwitchesAtOnce(t *testing.T) {
	m, backend := newPermissionsModel(t, agent.ModeManual)
	awaitApproval(t, m, 80)

	fullAccessIndex := slices.IndexFunc(approvalChoices[:], func(c approvalChoice) bool { return c.fullAccess() })
	for range fullAccessIndex {
		_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	}
	_, _ = m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if waitingApprovals(m) != 0 {
		t.Fatal("full access did not answer the pending call")
	}
	if got := m.tools.PermissionsMode(); got != agent.ModeSkipPermissions {
		t.Fatalf("mode = %q, want skip-permissions as soon as it is chosen", got)
	}
	if fullAccessNotices(m) != 1 || !m.op.busy() {
		t.Fatalf("full access announced %d times mid-turn, want once", fullAccessNotices(m))
	}

	drainConversationRemote(t, m)
	if got := fullAccessNotices(m); got != 1 {
		t.Fatalf("full access announced %d times, want once", got)
	}
	if backend.calls < 2 || !strings.Contains(backend.responses[0][0].Metadata.Output, `"status":"ok"`) {
		t.Fatalf("tool responses = %+v, want the call to have run", backend.responses)
	}
}

func fullAccessNotices(m *Model) int {
	want := permissionsChangedNotice(agent.ModeSkipPermissions).Text
	count := 0
	for _, n := range m.notices {
		if n.Notice.Text == want {
			count++
		}
	}
	return count
}

// TestApprovalChipKeepsBackgroundAfterBoldSegment guards the subtle failure
// mode of rendering the bold "full access" emphasis as a separate segment
// (see approvalChoice.chip): the rendered row inside the full panel (below)
// shows a single unbroken background behind "Switch to " + "full access" +
// its trailing padding — the row's background-painted span must cover the
// whole chip, not stop at the bold segment.
func TestApprovalChipKeepsBackgroundAfterBoldSegment(t *testing.T) {
	theme := styles.Default(true)
	style := theme.Approval.FullAccess
	fullAccessIndex := slices.IndexFunc(approvalChoices[:], func(c approvalChoice) bool { return c.fullAccess() })
	choice := approvalChoices[fullAccessIndex]
	chip := choice.chip(style, false)

	if plain := ansi.Strip(chip); plain != " "+choice.label+choice.emphasis+" " {
		t.Fatalf("chip plain text = %q, want %q", plain, " "+choice.label+choice.emphasis+" ")
	}
	// Each of the chip's three Render calls re-emits a truecolor background
	// SGR code ("48;2;..."); seeing it only once would mean the trailing
	// padding segment lost its background after the bold emphasis.
	if n := strings.Count(chip, "48;2;"); n < 2 {
		t.Fatalf("chip %q sets its truecolor background fewer than twice; want it reapplied after the bold segment", chip)
	}
}

// TestApprovalCompactFormKeepsFocusOnEveryChoice guards against the rejected
// full-form measurement clamping the focus: at these sizes the panel measures
// the full form (too narrow for Session and full access), rejects it, and
// shows the compact form with every choice. Tabbing onto each must stay there.
func TestApprovalCompactFormKeepsFocusOnEveryChoice(t *testing.T) {
	for _, size := range [][2]int{{36, 13}, {40, 14}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			backend := &approvalBackend{t: t}
			tools, err := agent.NewToolSet(agent.ModeManual, newApprovalTool())
			if err != nil {
				t.Fatal(err)
			}
			model := New(agent.New(backend, agent.Config{}), Config{Tools: tools})
			awaitApproval(t, model, size[0])
			model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})

			panel := model.approvals[model.shown.callID()].panel
			if !slices.Equal(panel.visible, allChoiceIndices()) {
				t.Fatalf("visible = %v, want every choice in the compact form", panel.visible)
			}
			for want := 1; want < len(approvalChoices); want++ {
				model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
				if panel.selected != want {
					t.Fatalf("selected = %d, want %d after %d tabs", panel.selected, want, want)
				}
				plain := ansi.Strip(model.frame.promptView)
				if approvalChoices[want].fullAccess() != strings.Contains(plain, "Full access") {
					t.Fatalf("disclaimer shown = %v with %q focused:\n%s", !approvalChoices[want].fullAccess(), approvalChoices[want].compactLabel, plain)
				}
			}
		})
	}
}

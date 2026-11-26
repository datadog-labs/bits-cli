package chat

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/filediff"
	"github.com/datadog-labs/bits-cli/internal/tools/spec"
	"github.com/datadog-labs/bits-cli/internal/tui/components"
	"github.com/datadog-labs/bits-cli/internal/tui/diffrender"
	"github.com/datadog-labs/bits-cli/internal/tui/escape"
	"github.com/datadog-labs/bits-cli/internal/tui/styles"
	"github.com/datadog-labs/bits-cli/internal/tui/textwrap"
)

const (
	genericOutputMaxLines = 3
	execCommandMaxLines   = 10
	execOutputMaxLines    = 5
	collapsedDiffLines    = 15
)

// toolGroup joins adjacent compatible calls, and the reasoning between them,
// under one header.
type toolGroup struct {
	name string
	// closed groups show only their work in progress until opened: running
	// calls and the live reasoning. Open groups always show every child.
	closed bool
}

var (
	inspectionGroup = &toolGroup{name: "Files", closed: true}
	// Edits stay open: their diffs are what the user most needs to review.
	editGroup = &toolGroup{name: "Edits"}
	// skillGroup is neutral: skill loads join whichever group surrounds them.
	skillGroup  = &toolGroup{name: "Skills", closed: true}
	serverGroup = &toolGroup{name: "Tools", closed: true}
)

// toolPresentation is derived solely for rendering. The source ToolBlock stays
// authoritative and unchanged for execution, persistence, and headless output.
type toolPresentation struct {
	identity   spec.Identity
	name       string
	argument   string
	context    string
	timeout    string
	validInput bool
	// code is a code tool's script, escaped for display.
	code string
	// renderSpec is never nil: unknown tools use their client or server fallback.
	renderSpec *toolRenderSpec
	// action is renderSpec's verbs, adjusted to what a command tool's result
	// says happened.
	action toolAction
	// result is a command tool's decoded result, nil until it has one.
	result *spec.ExecCommandOutput
}

// group returns the group this call joins, or nil when it stands alone. Local
// inspections require decodable input; server tools can group while their
// arguments are still streaming.
func (p toolPresentation) group(tool *agent.ToolBlock) *toolGroup {
	if tool.Status == agent.ToolAwaitingApproval || tool.Approval != nil {
		return nil
	}
	if !p.validInput {
		return nil
	}
	return p.renderSpec.group
}

// toolRenderSpec keeps a tool renderer and its static transcript layout plan
// together. The presentation layer selects it before rendering so List can
// account for spacing during lazy height and scroll calculations.
//
// A toolRenderFunc renders the requested disclosure view. Only write/edit and
// exec_command have a compact view beyond their header.
type (
	toolRenderFunc         func(tool *agent.ToolBlock, p toolPresentation, c renderContext) string
	toolApprovalRenderFunc func(*agent.ToolBlock, toolPresentation, int, Styles) string
)

// toolApprovalSpec is a tool's own design for the prompt approving a call:
// its title, or nil to keep the prompt's, and its body, or nil to keep the
// prompt's detail. It applies to a server tool an approval_request gates too.
type toolApprovalSpec struct {
	title  func(*agent.ToolBlock, toolPresentation) string
	render toolApprovalRenderFunc
}

// toolAction contains the human-facing verb forms for a local tool. Empty
// lifecycle forms fall back to base, which keeps tools with quiet terminal
// states compact while allowing active work to use a natural progressive verb.
type toolAction struct {
	base    string
	active  string
	success string
	failure string
}

type toolRenderSpec struct {
	render   toolRenderFunc
	approval *toolApprovalSpec // nil: the approval prompt's own title and detail
	spacing  itemSpacing
	action   toolAction
	// group joins compatible calls and intervening reasoning under one
	// header; nil keeps each call on its own.
	group *toolGroup
	// static marks a renderer that ignores disclosure, so List offers no
	// disclosure control.
	static   bool
	interact func(call agent.ToolCall) ToolPrompt // nil = not interactive
}

// ToolPrompt is the interactive UI a client tool asks the user through. The
// host shows it like a tool approval, and hands Result to the waiting tool.
type ToolPrompt interface {
	components.Prompt
	// Result returns the user's answer; the tool converts it to a call result.
	Result() (any, bool)
}

// NewToolPrompt builds the interactive UI registered for a client tool.
func NewToolPrompt(call agent.ToolCall) (ToolPrompt, bool) {
	renderSpec := toolRenderSpecFor(spec.Identity{ClientSide: true, Name: call.Name})
	if renderSpec.interact == nil {
		return nil, false
	}
	prompt := renderSpec.interact(call)
	return prompt, prompt != nil
}

var (
	serverToolRenderSpec       = &toolRenderSpec{render: renderServerTool, group: serverGroup}
	questionToolRenderSpec     = &toolRenderSpec{render: renderQuestionsTool, spacing: itemSpacing{before: 1, after: 1}, static: true, interact: newQuestionPrompt}
	simpleToolRenderSpec       = &toolRenderSpec{render: renderSimpleTool}
	spawnAgentRenderSpec       = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Start subagent", active: "Starting subagent", success: "Started subagent", failure: "Subagent start failed"}}
	sendAgentMessageRenderSpec = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Send message", active: "Sending message", success: "Sent message", failure: "Message failed"}}
	waitForAgentsRenderSpec    = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Wait for", active: "Waiting for", success: "Waited for", failure: "Wait failed"}}
	readToolRenderSpec         = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Read", active: "Reading"}, group: inspectionGroup}
	listToolRenderSpec         = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "List", active: "Listing"}, group: inspectionGroup}
	grepToolRenderSpec         = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Search", active: "Searching"}, group: inspectionGroup}
	writeToolRenderSpec        = &toolRenderSpec{
		render:  renderChangeTool,
		group:   editGroup,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "Write", active: "Writing", success: "Wrote", failure: "Write failed"},
	}
	editToolRenderSpec = &toolRenderSpec{
		render:  renderChangeTool,
		group:   editGroup,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "Edit", active: "Editing", success: "Edited", failure: "Edit failed"},
	}
	execToolRenderSpec = &toolRenderSpec{
		render:   renderExecTool,
		approval: &toolApprovalSpec{render: renderExecApproval},
		spacing:  itemSpacing{before: 1, after: 1},
		action:   toolAction{base: "Run", success: "Ran", failure: "Run failed"},
	}
	waitCommandRenderSpec = &toolRenderSpec{
		render:  renderCommandControlTool,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "Wait for command", active: "Waiting for command", success: "Waited for", failure: "Wait failed"},
	}
	stopCommandRenderSpec = &toolRenderSpec{
		render:  renderCommandControlTool,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "Stop command", active: "Stopping command", success: "Stopped", failure: "Stop failed"},
	}
	skillToolRenderSpec = &toolRenderSpec{
		render: renderSimpleTool,
		group:  skillGroup,
		action: toolAction{base: "Load", active: "Loading", success: "Loaded", failure: "Load failed"},
	}
	startBackgroundTaskRenderSpec = &toolRenderSpec{
		render:   renderSimpleTool,
		approval: &toolApprovalSpec{title: backgroundTaskApprovalTitle, render: renderBackgroundTaskApproval},
		action:   toolAction{base: "Start background task", active: "Starting background task", success: "Started background task", failure: "Background task failed"},
	}
	handoffToolRenderSpec = &toolRenderSpec{
		render: renderSimpleTool,
		action: toolAction{base: "Continue in terminal", active: "Continuing in terminal", success: "Continued in terminal", failure: "Handoff failed"},
	}
	// Code tools read Datadog data like the other server tools, so they join
	// their group: the answer matters more than the scripts behind it.
	executeCodeRenderSpec = &toolRenderSpec{
		render: renderCodeTool,
		group:  serverGroup,
		action: toolAction{base: "Run code", active: "Running code", success: "Ran code", failure: "Code failed"},
	}
	searchDatadogSDKRenderSpec = &toolRenderSpec{
		render: renderCodeTool,
		group:  serverGroup,
		action: toolAction{base: "Search Datadog SDK", active: "Searching Datadog SDK", success: "Searched Datadog SDK", failure: "Datadog SDK search failed"},
	}
)

type summarySpan struct {
	text string
	kind spanKind
}

type spanKind uint8

const (
	spanAction spanKind = iota
	spanArgument
	spanMuted
	spanError
)

func (a toolAction) label(state agent.ToolStatus) string {
	var label string
	switch state {
	case agent.ToolRunning:
		label = a.active
	case agent.ToolSuccess:
		label = a.success
	case agent.ToolError:
		label = a.failure
	case agent.ToolUnknown, agent.ToolAwaitingApproval, agent.ToolDenied, agent.ToolCancelled:
		// A call awaiting approval has not begun.
		label = a.base
	}
	if label == "" {
		return a.base
	}
	return label
}

func statusOf(tool *agent.ToolBlock) agent.ToolStatus {
	if tool == nil {
		return agent.ToolUnknown
	}
	return tool.Status
}

// classifyTool uses the complete wire identity. In particular, server tools
// with names matching local tools deliberately remain generic. An
// approval_request presents the call it gates.
//
// TODO: Presentation input decoding is repeated when the transcript view
// rebuilds.
func classifyTool(tool *agent.ToolBlock) toolPresentation {
	tool = approvalSubject(tool)
	if tool == nil {
		return toolPresentation{renderSpec: simpleToolRenderSpec}
	}
	qualified := qualifiedToolName(tool)
	input := toolInput(tool)
	id := tool.Identity()
	p := toolPresentation{identity: id, name: qualified, renderSpec: toolRenderSpecFor(id)}
	p.action = p.renderSpec.action

	switch id {
	case spec.ClientReadFile, spec.ClientWriteFile, spec.ClientEditFile:
		if in, ok := decode[spec.PathInput](input); ok && in.Path != "" {
			p.argument, p.validInput = escape.Inline(in.Path), true
		}
	case spec.ClientListFiles:
		if in, ok := decode[spec.ListFilesInput](input); ok {
			if in.Path == "" {
				in.Path = "."
			}
			p.argument, p.validInput = escape.Inline(in.Path), true
			if in.Depth != nil {
				p.context = fmt.Sprintf("depth %d", *in.Depth)
			}
		}
	case spec.ClientGrepFiles:
		if in, ok := decode[spec.GrepFilesInput](input); ok && in.Pattern != "" {
			p.argument, p.context = escape.Inline(in.Pattern), escape.Inline(in.Path)
			p.validInput = true
		}
	case spec.ClientExecCommand:
		if in, ok := decode[spec.ExecCommandInput](input); ok && in.Cmd != "" {
			// Preserve command line boundaries for the exec-specific renderer. The
			// working directory remains compact metadata in the header.
			if in.Workdir == "" {
				in.Workdir = "."
			}
			p.argument, p.context = escape.Multiline(in.Cmd), escape.SingleLine(in.Workdir)
			if in.TimeoutMS != nil && *in.TimeoutMS > 0 {
				p.timeout = (time.Duration(*in.TimeoutMS) * time.Millisecond).String()
			}
			p.validInput = true
		}
		p.result = decodeExecResult(tool)
		if p.result != nil && p.result.Status == spec.ExecRunning {
			// The call answered while the command kept running: it started,
			// it has not run to completion.
			p.action.success = "Started"
		}
	case spec.ClientWaitCommand, spec.ClientStopCommand:
		// The id means nothing to the user: the row names the command once
		// the result does.
		p.validInput = true
		p.result = decodeExecResult(tool)
		if p.result != nil {
			command, _, _ := strings.Cut(p.result.Command, "\n")
			p.argument = escape.Inline(command)
			// The wait or stop itself worked: a failure is the command's,
			// which the error glyph and the suffix already show.
			p.action.failure = p.action.success
		}
	case spec.ServerSpawnAgent:
		if in, ok := decode[spec.SpawnAgentInput](input); ok && in.Prompt != "" {
			p.argument, p.validInput = escape.Inline(in.Prompt), true
		}
	case spec.ServerSendAgentMessage:
		if in, ok := decode[spec.SendAgentMessageInput](input); ok && in.Message != "" {
			p.argument, p.validInput = escape.Inline(in.Message), true
		}
	case spec.ServerWaitForAgents:
		if in, ok := decode[spec.WaitForAgentsInput](input); ok && len(in.AgentIDs) > 0 {
			p.argument = count(len(in.AgentIDs), "subagent", "subagents")
			if in.Mode == "any" && len(in.AgentIDs) > 1 {
				p.argument = "any of " + p.argument
			}
			p.validInput = true
		}
	case spec.ServerSkill:
		p.argument = "skill"
		if in, ok := decode[spec.SkillInput](input); ok && in.SkillName != "" {
			p.argument = "skill " + escape.Inline(in.SkillName)
		}
		p.validInput = true
	case spec.ServerStartBackgroundTask:
		if in, ok := backgroundTaskInput(input); ok {
			p.argument, p.validInput = escape.Inline(cmp.Or(in.Task.TaskName, BackgroundAgentName(in.Task.AgentID))), true
		}
	case spec.ServerExecuteCode, spec.ServerSearchDatadogSDK:
		// The server's detail renders the script as Markdown on some
		// endpoints only, so the input is the one source of the script.
		if in, ok := decode[spec.CodeInput](input); ok {
			p.code, p.argument = escape.Multiline(in.Code), escape.Inline(in.Telemetry.Intent)
		}
		// Like other server tools, a call joins its group while its input
		// still streams.
		p.validInput = true
	case spec.ClientHandoff:
		if in, ok := decode[spec.HandoffInput](input); ok && in.Reason != "" {
			p.argument = "Reason: " + escape.Inline(in.Reason)
		}
		p.validInput = true
	default:
		p.argument, p.validInput = compactInput(input), true
	}
	return p
}

func toolRenderSpecFor(id spec.Identity) *toolRenderSpec {
	switch id {
	case spec.ClientAskUserQuestion:
		return questionToolRenderSpec
	case spec.ClientReadFile:
		return readToolRenderSpec
	case spec.ClientListFiles:
		return listToolRenderSpec
	case spec.ClientGrepFiles:
		return grepToolRenderSpec
	case spec.ClientWriteFile:
		return writeToolRenderSpec
	case spec.ClientEditFile:
		return editToolRenderSpec
	case spec.ClientExecCommand:
		return execToolRenderSpec
	case spec.ClientWaitCommand:
		return waitCommandRenderSpec
	case spec.ClientStopCommand:
		return stopCommandRenderSpec
	case spec.ServerSpawnAgent:
		return spawnAgentRenderSpec
	case spec.ServerSendAgentMessage:
		return sendAgentMessageRenderSpec
	case spec.ServerWaitForAgents:
		return waitForAgentsRenderSpec
	case spec.ServerSkill:
		return skillToolRenderSpec
	case spec.ServerStartBackgroundTask:
		return startBackgroundTaskRenderSpec
	case spec.ClientHandoff:
		return handoffToolRenderSpec
	case spec.ServerExecuteCode:
		return executeCodeRenderSpec
	case spec.ServerSearchDatadogSDK:
		return searchDatadogSDKRenderSpec
	default:
		if !id.ClientSide {
			return serverToolRenderSpec
		}
		return simpleToolRenderSpec
	}
}

// decode reads a JSON object payload; ok is false for anything else.
func decode[T any](payload string) (v T, ok bool) {
	payload = strings.TrimSpace(payload)
	if !strings.HasPrefix(payload, "{") {
		return v, false
	}
	return v, json.Unmarshal([]byte(payload), &v) == nil
}

func toolInput(tool *agent.ToolBlock) string {
	if tool.HasFinalInput || tool.Input != "" {
		return tool.Input
	}
	return tool.InputPartial
}

func qualifiedToolName(tool *agent.ToolBlock) string {
	name := tool.Name
	if name == "" {
		name = "tool"
	}
	if namespace := tool.Identity().Namespace; namespace != "" {
		name = namespace + "." + name
	}
	return escape.Inline(name)
}

func compactInput(input string) string {
	if strings.TrimSpace(input) == "" {
		return "{}"
	}
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(input)) == nil {
		return escape.Inline(buf.String())
	}
	return escape.Inline(collapseWS(input))
}

func (p toolPresentation) summary(tool *agent.ToolBlock) []summarySpan {
	state := statusOf(tool)
	action := p.actionLabel(state)
	if !p.validInput {
		// Keep known tools recognizable while input is streaming or malformed.
		// Exact identity selects the action; arguments remain omitted until
		// decoding succeeds. Other tools retain their qualified name.
		if action != "" {
			return actionArgument(action, "")
		}
		return []summarySpan{{text: p.name, kind: spanAction}}
	}
	switch p.identity {
	case spec.ClientListFiles:
		spans := actionArgument(action, p.argument)
		if p.context != "" {
			spans = append(spans, summarySpan{text: " · ", kind: spanMuted}, summarySpan{text: p.context, kind: spanMuted})
		}
		return spans
	case spec.ClientGrepFiles:
		spans := actionArgument(action, p.argument)
		if p.context != "" && p.context != "." {
			spans = append(spans, summarySpan{text: " in ", kind: spanMuted}, summarySpan{text: p.context, kind: spanArgument})
		}
		return spans
	case spec.ClientExecCommand:
		return append(actionArgument(action, p.argument), p.execMeta()...)
	case spec.ServerExecuteCode, spec.ServerSearchDatadogSDK:
		// The intent the model gave reads as a caption, not an argument.
		spans := []summarySpan{{text: action, kind: spanAction}}
		if p.argument != "" {
			spans = append(spans, summarySpan{text: " · ", kind: spanMuted}, summarySpan{text: p.argument, kind: spanArgument})
		}
		return spans
	default:
		if action != "" {
			return actionArgument(action, p.argument)
		}
		return []summarySpan{{text: p.name, kind: spanAction}, {text: "(" + p.argument + ")", kind: spanArgument}}
	}
}

// execMeta is the metadata the exec header shows after the command.
func (p toolPresentation) execMeta() []summarySpan {
	var spans []summarySpan
	if p.context != "" {
		spans = append(spans, summarySpan{text: " in ", kind: spanMuted}, summarySpan{text: p.context, kind: spanArgument})
	}
	if p.timeout != "" {
		spans = append(spans, summarySpan{text: " · timeout ", kind: spanMuted}, summarySpan{text: p.timeout, kind: spanArgument})
	}
	return spans
}

func (p toolPresentation) actionLabel(state agent.ToolStatus) string {
	return p.action.label(state)
}

func actionArgument(action, argument string) []summarySpan {
	spans := []summarySpan{{text: action, kind: spanAction}}
	if argument != "" {
		spans = append(spans, summarySpan{text: " ", kind: spanMuted}, summarySpan{text: argument, kind: spanArgument})
	}
	return spans
}

// renderTool renders an individual call in its compact view. Inspection
// grouping is performed by List before this point; RenderBlock intentionally
// remains a one-block API.
func renderTool(it agent.Block, width int, sty Styles, frame int) string {
	if it.Tool == nil {
		return fallback(it, width, sty)
	}
	return renderPresentedTool(it.Tool, classifyTool(it.Tool), renderContext{width: width, sty: sty, frame: frame})
}

// renderPresentedTool renders the call tool presents, as classifyTool did.
func renderPresentedTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	return p.renderSpec.render(approvalSubject(tool), p, c)
}

// ToolApproval is a tool's own design for its approval prompt. The caller
// keeps the surrounding panel and actions, and its generic title and detail
// where the design has none: an empty Title, a nil Render.
type ToolApproval struct {
	Title  string
	Render func(width int) string
}

// ApprovalOf returns the approval design of the call a pending tool block asks
// to approve: the server call an approval_request gates, or the tool itself.
func ApprovalOf(tool *agent.ToolBlock, sty Styles) ToolApproval {
	subject := approvalSubject(tool)
	p := classifyTool(subject)
	design := p.renderSpec.approval
	if design == nil || !p.validInput {
		return ToolApproval{}
	}
	var approval ToolApproval
	if design.title != nil {
		approval.Title = design.title(subject, p)
	}
	if design.render != nil {
		approval.Render = func(width int) string { return design.render(subject, p, max(1, width), sty) }
	}
	return approval
}

// approvalSubject returns the server call an approval_request tool gates, in
// the request's state and with its result, or tool itself.
func approvalSubject(tool *agent.ToolBlock) *agent.ToolBlock {
	gated, ok := tool.GatedCall()
	if !ok {
		return tool
	}
	subject := &agent.ToolBlock{
		Name: gated.Name, Input: gated.Input, HasFinalInput: true,
		Status: tool.Status, Approval: tool.Approval,
		Title: tool.Title, Detail: tool.Detail, Output: tool.Output,
	}
	if tool.Status == agent.ToolSuccess {
		// A request succeeds once approved, before the server runs the call:
		// until the call's own result replaces the request, it has not begun,
		// and the approval's result says nothing about it.
		subject.Status, subject.Title, subject.Detail, subject.Output = agent.ToolUnknown, "", "", ""
	}
	return subject
}

// renderToolGroupHeader counts tool calls, while reasoning only contributes
// to the running state. List decides which children show beneath it.
func renderToolGroupHeader(group *toolGroup, blocks []agent.Block, presentations []toolPresentation, c renderContext) string {
	counts := make(map[agent.ToolStatus]int)
	files := make(map[string]struct{})
	calls, changes, skills := 0, 0, 0
	reasoning := false
	for i, block := range blocks {
		if block.Kind == assistant.KindReasoning {
			reasoning = reasoning || !block.Complete
			continue
		}
		calls++
		counts[statusOf(block.Tool)]++
		p := presentations[i]
		if p.identity == spec.ServerSkill {
			skills++
		}
		if p.renderSpec.group == editGroup {
			changes++
			if p.argument != "" {
				files[p.argument] = struct{}{}
			}
		}
	}
	state := agent.ToolUnknown
	switch {
	case counts[agent.ToolRunning] > 0 || reasoning:
		state = agent.ToolRunning
	case counts[agent.ToolError] > 0:
		state = agent.ToolError
	case counts[agent.ToolSuccess] == calls:
		state = agent.ToolSuccess
	}
	suffix := " · " + count(calls, "call", "calls")
	if group == editGroup {
		suffix = " · " + count(changes, "change", "changes") + " across " + count(len(files), "file", "files")
		if skills > 0 {
			suffix += " · " + count(skills, "skill", "skills")
		}
	}
	for _, outcome := range []struct {
		status agent.ToolStatus
		label  string
	}{
		{agent.ToolRunning, "running"},
		{agent.ToolError, "failed"},
		{agent.ToolCancelled, "stopped"},
		{agent.ToolDenied, "denied"},
	} {
		if n := counts[outcome.status]; n > 0 {
			suffix += fmt.Sprintf(" · %d %s", n, outcome.label)
		}
	}
	if reasoning {
		suffix += " · reasoning"
	}
	title := group.name
	if group == serverGroup {
		title = "Analyzed"
		if state == agent.ToolRunning {
			title = "Analyzing"
		}
	}
	return renderActivityHeader(state, title, suffix, c)
}

func renderServerTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	summary := p.summary(tool)
	if title := strings.TrimSpace(escape.SingleLine(tool.Title)); title != "" {
		summary = []summarySpan{{text: title, kind: spanAction}}
	}
	header := renderToolHeader(tool, summary, nil, c)
	if c.disclosure == compactView {
		if statusOf(tool) != agent.ToolError {
			return header
		}
		reason := tool.Detail
		if strings.TrimSpace(reason) == "" {
			reason = serverReadableOutput(tool.Output)
		}
		if strings.TrimSpace(reason) == "" {
			reason = "Tool failed"
		}
		return header + "\n" + renderReason(reason, c)
	}
	var sections []string
	if detail := strings.TrimSpace(tool.Detail); detail != "" && detail != strings.TrimSpace(tool.Output) {
		sections = append(sections, detail)
	} else if strings.TrimSpace(tool.Detail) == "" {
		if readable := serverMCPText(tool.Output); readable != "" {
			sections = append(sections, readable)
		}
	}
	sections = append(sections, "Tool: "+p.name)
	inputLabel := "Input:"
	if !tool.HasFinalInput && tool.Input == "" && tool.InputPartial != "" {
		inputLabel = "Input (partial):"
		if tool.InputPreviewTruncated {
			inputLabel = "Input (partial, truncated):"
		}
	}
	input := toolInput(tool)
	if strings.TrimSpace(input) == "" {
		input = "(no input)"
	}
	sections = append(sections, inputLabel+"\n"+indentServerPayload(input))
	output := tool.Output
	if strings.TrimSpace(output) != "" {
		sections = append(sections, "Output:\n"+indentServerPayload(output))
	} else if statusOf(tool) == agent.ToolSuccess || statusOf(tool) == agent.ToolError {
		sections = append(sections, "Output:\n  (no output)")
	}
	return header + "\n" + renderPreview(strings.Join(sections, "\n\n"), genericOutputMaxLines, detailStyle(statusOf(tool), c.sty), false, c)
}

func indentServerPayload(payload string) string {
	var buf bytes.Buffer
	if json.Indent(&buf, []byte(payload), "", "  ") == nil {
		payload = buf.String()
	}
	return "  " + strings.ReplaceAll(payload, "\n", "\n  ")
}

func serverReadableOutput(output string) string {
	if text := serverMCPText(output); text != "" {
		return text
	}
	return output
}

// Only unwrap recognized text blocks. Mixed/image/resource content remains
// available in the original output; no structured result data is discarded.
func serverMCPText(output string) string {
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var envelope struct {
		Content []block `json:"content"`
	}
	var blocks []block
	if json.Unmarshal([]byte(output), &envelope) == nil {
		blocks = envelope.Content
	}
	if blocks == nil {
		if json.Unmarshal([]byte(output), &blocks) != nil {
			return ""
		}
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// renderSimpleTool shows only the header until expanded. The full view adds
// the whole detail or output, or "(no output)" once the call has settled.
func renderSimpleTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	header := renderToolHeader(tool, p.summary(tool), nil, c)
	if c.disclosure == compactView {
		if statusOf(tool) == agent.ToolError {
			reason := tool.Detail
			if reason == "" {
				reason = tool.Output
			}
			if reason == "" {
				reason = "failed"
			}
			return header + "\n" + renderReason(reason, c)
		}
		return header
	}
	detail := tool.Detail
	if detail == "" {
		detail = tool.Output
	}
	if body := renderDetail(detail, statusOf(tool), genericOutputMaxLines, c); body != "" {
		return header + "\n" + body
	}
	return header
}

// renderDetail renders a tool's detail text, clamped to limit rows in the
// compact view. Empty detail renders "(no output)" once the call has settled,
// and nothing while it is still running or awaiting approval.
func renderDetail(detail string, state agent.ToolStatus, limit int, c renderContext) string {
	if detail == "" {
		if state == agent.ToolRunning || state == agent.ToolAwaitingApproval {
			return ""
		}
		return renderNoOutput(c.width, c.sty)
	}
	return renderPreview(detail, limit, detailStyle(state, c.sty), false, c)
}

// renderChangeTool shows the newest collapsedDiffLines of the diff in its
// compact view and the whole diff when expanded.
func renderChangeTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	path := p.argument
	var diff *filediff.Diff
	var fallbackDetail string
	if state, ok := tool.RenderState.(*filediff.State); ok && state != nil {
		path, diff, fallbackDetail = editorDiff(state)
	} else if parsed, ok := filediff.ParseUnifiedDiff(tool.Detail); ok {
		diff = &parsed
		if path == "" {
			path = strings.TrimPrefix(parsed.To, "b/")
		}
	}
	if path != "" {
		p.argument, p.validInput = escape.Inline(path), true
	}
	header := renderToolHeader(tool, p.summary(tool), nil, c)
	if diff != nil {
		lines := []string{header}
		if format := formatChange(*diff); format != "" {
			lines = append(lines, c.sty.ToolDetail.Render(ansi.Truncate("  └ "+format, c.width, "…")))
		}
		limit := collapsedDiffLines
		if c.disclosure == fullView {
			// TODO: bound very large expanded diffs if rendering cost shows up.
			limit = 0
		}
		body := diffrender.Render(*diff, diffrender.Options{Path: path, Width: c.width, Style: c.sty.Diff, MaxLines: limit, Tail: true, Wrap: c.disclosure == fullView})
		if body != "" {
			lines = append(lines, body)
		}
		return strings.Join(lines, "\n")
	}

	if fallbackDetail == "" {
		fallbackDetail = tool.Detail
	}
	if fallbackDetail == "" {
		fallbackDetail = tool.Output
	}
	state := statusOf(tool)
	if c.disclosure == compactView {
		switch state {
		case agent.ToolRunning, agent.ToolDenied, agent.ToolCancelled:
			return header
		case agent.ToolUnknown, agent.ToolAwaitingApproval, agent.ToolSuccess, agent.ToolError:
		}
		if fallbackDetail == "" {
			return header
		}
	}
	if body := renderDetail(fallbackDetail, state, genericOutputMaxLines, c); body != "" {
		return header + "\n" + body
	}
	return header
}

func editorDiff(state *filediff.State) (path string, diff *filediff.Diff, reason string) {
	switch {
	case state.Change != nil:
		return state.Change.Path, state.Change.Diff, state.Reason
	case state.Preview != nil:
		if state.Snapshot != nil {
			path = state.Snapshot.Path
		}
		return path, state.Preview.Diff, state.Reason
	case state.Snapshot != nil:
		return state.Snapshot.Path, nil, state.Reason
	default:
		return "", nil, state.Reason
	}
}

func formatChange(diff filediff.Diff) string {
	if diff.BeforeFormat == diff.AfterFormat {
		return ""
	}
	return "format: " + formatName(diff.BeforeFormat) + " → " + formatName(diff.AfterFormat)
}

func formatName(format filediff.TextFormat) string {
	name := strings.ToUpper(format.LineEnding)
	if format.BOM {
		name += " + BOM"
	}
	return name
}

func renderExecTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	return renderExecBody(tool, p.result, renderExecInvocation(tool, p, execSuffix(p.result), c), c)
}

// renderCommandControlTool shows a wait or stop: the command it targets, once
// the result names it, then what it printed since the previous result.
func renderCommandControlTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	return renderExecBody(tool, p.result, renderToolHeader(tool, p.summary(tool), execSuffix(p.result), c), c)
}

// decodeExecResult returns a command tool's result, nil until it has a
// decodable one.
func decodeExecResult(tool *agent.ToolBlock) *spec.ExecCommandOutput {
	result, ok := decode[spec.ExecCommandOutput](tool.Output)
	if !ok {
		return nil
	}
	return &result
}

// renderExecBody follows a command tool's header with the command output.
func renderExecBody(tool *agent.ToolBlock, result *spec.ExecCommandOutput, header string, c renderContext) string {
	state := statusOf(tool)
	if tool.Output == "" {
		return header
	}
	switch state {
	case agent.ToolRunning, agent.ToolAwaitingApproval, agent.ToolDenied:
		return header
	case agent.ToolCancelled:
		if result != nil && execResultIsTerminal(result.Status) && len(execRows(*result, max(1, c.width-4), compactView)) == 0 {
			return header + "\n" + renderNoOutput(c.width, c.sty)
		}
		return header
	case agent.ToolUnknown, agent.ToolSuccess, agent.ToolError:
	}
	style := detailStyle(state, c.sty)
	if result == nil {
		return header + "\n" + renderPreview(tool.Output, execOutputMaxLines, style, true, c)
	}
	rows := execRows(*result, max(1, c.width-4), c.disclosure)
	if len(rows) == 0 {
		if !execResultIsTerminal(result.Status) {
			return header
		}
		return header + "\n" + renderNoOutput(c.width, c.sty)
	}
	return header + "\n" + renderRows(rows, c.width, style, c.sty)
}

func renderExecApproval(tool *agent.ToolBlock, p toolPresentation, width int, sty Styles) string {
	command := strings.TrimRight(p.argument, "\n")
	var rows []string
	for _, line := range highlightShellCommand(command, sty) {
		rows = append(rows, textwrap.Wrap(textwrap.Options{Width: width, RestPrefix: "  ", Breakpoints: textwrap.Shell, KeepIndent: true}, line...)...)
	}

	detail := ""
	if tool.Approval != nil {
		detail = escape.Inline(tool.Approval.Detail)
	}
	if detail != "" {
		rows = append(rows, textwrap.WrapProse(width, detail, sty.ToolDetail)...)
	}
	return strings.Join(rows, "\n")
}

// decodeCodeResult returns a code tool's result, nil until it has a decodable
// one. A catalog search answers with the script's value alone.
func decodeCodeResult(tool *agent.ToolBlock, id spec.Identity) *spec.ExecuteCodeOutput {
	if id == spec.ServerSearchDatadogSDK {
		if statusOf(tool) != agent.ToolSuccess || !json.Valid([]byte(tool.Output)) {
			return nil
		}
		return &spec.ExecuteCodeOutput{Value: json.RawMessage(tool.Output)}
	}
	result, ok := decode[spec.ExecuteCodeOutput](tool.Output)
	if !ok || result.Outcome == "" {
		return nil
	}
	return &result
}

// renderCodeTool keeps a code tool to its header until expanded: the intent
// says what the script does. The full view shows the script, then what it
// logged and returned, or its error.
func renderCodeTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	result := decodeCodeResult(tool, p.identity)
	rows := []string{renderToolHeader(tool, p.summary(tool), codeSuffix(result), c)}
	if c.disclosure == compactView {
		return rows[0]
	}
	if p.code != "" {
		code := diffrender.Highlight("script.js", strings.TrimRight(p.code, "\n"), c.sty.Diff.SyntaxDark, c.sty.ToolArgument)
		rows = append(rows, renderSourceBranch(code, 0, textwrap.Code, c)...)
	}
	if body := renderCodeOutput(tool, result, c); body != "" {
		rows = append(rows, body)
	}
	return strings.Join(rows, "\n")
}

func renderCodeOutput(tool *agent.ToolBlock, result *spec.ExecuteCodeOutput, c renderContext) string {
	state := statusOf(tool)
	switch state {
	case agent.ToolRunning, agent.ToolAwaitingApproval, agent.ToolDenied, agent.ToolCancelled:
		return ""
	case agent.ToolUnknown, agent.ToolSuccess, agent.ToolError:
	}
	if result == nil {
		// NOTE: The multi-agent endpoint sends no result, and failures come
		// as the backend's error text, so neither has rows to decode.
		output := serverReadableOutput(tool.Output)
		if strings.TrimSpace(output) == "" {
			return ""
		}
		return renderDetail(output, state, 0, c)
	}
	rows := codeRows(*result, max(1, c.width-4))
	if len(rows) == 0 {
		return renderNoOutput(c.width, c.sty)
	}
	return renderRows(rows, c.width, detailStyle(state, c.sty), c.sty)
}

// codeRows lays out what a script logged, then its error or the value it
// returned as indented JSON, then the server's caveats about it.
func codeRows(result spec.ExecuteCodeOutput, bodyWidth int) []string {
	var rows []string
	for _, line := range result.Console {
		rows = append(rows, wrappedRows(escape.Multiline(line), bodyWidth)...)
	}
	rows = append(rows, wrappedRows(escape.Multiline(result.Error), bodyWidth)...)
	var value bytes.Buffer
	if json.Indent(&value, result.Value, "", "  ") == nil {
		rows = append(rows, textwrap.Wrap(textwrap.Options{Width: bodyWidth, Breakpoints: textwrap.Code, KeepIndent: true}, textwrap.Plain(escape.Multiline(value.String())))...)
	}
	if sampled := result.Diagnostics.Sampled; sampled != nil {
		rows = append(rows, wrappedRows(escape.Multiline(sampled.Note), bodyWidth)...)
	}
	if result.Truncated {
		rows = append(rows, "… console truncated")
	}
	return rows
}

// codeSuffix says when the script's API calls failed: its result may then be
// partial without the value saying so.
func codeSuffix(result *spec.ExecuteCodeOutput) []summarySpan {
	if result == nil || result.Diagnostics.APICalls.Failed == 0 {
		return nil
	}
	failed := count(int(result.Diagnostics.APICalls.Failed), "failed API call", "failed API calls")
	return []summarySpan{{text: " · " + failed, kind: spanMuted}}
}

// backgroundTaskInput decodes a startBackgroundTask call's task.
func backgroundTaskInput(input string) (spec.StartBackgroundTaskInput, bool) {
	in, ok := decode[spec.StartBackgroundTaskInput](input)
	return in, ok && strings.TrimSpace(in.Task.AgentID) != ""
}

// BackgroundAgentName names the server agent a background task runs on.
func BackgroundAgentName(agentID string) string {
	if agentID == "investigation" {
		return "Bits Investigation"
	}
	return escape.Inline(agentID)
}

func backgroundTaskApprovalTitle(tool *agent.ToolBlock, _ toolPresentation) string {
	if in, _ := backgroundTaskInput(tool.Input); in.Task.AgentID == "investigation" {
		return "Start a Bits Investigation?"
	}
	return "Start a background task?"
}

// renderBackgroundTaskApproval lists what the task will look into.
func renderBackgroundTaskApproval(tool *agent.ToolBlock, _ toolPresentation, width int, sty Styles) string {
	in, _ := backgroundTaskInput(tool.Input)
	task := in.Task
	investigation := task.AgentID == "investigation"
	var lines []string
	add := func(label, value string) {
		if value = escape.SingleLine(value); value != "" {
			lines = append(lines, label+": "+value)
		}
	}
	if !investigation {
		add("Agent", task.AgentID)
	}
	add("Task", task.TaskName)
	p := task.Payload
	add("Investigate", p.Description)
	if p.IncidentPublicID != nil {
		add("Incident", fmt.Sprintf("IR-%d", *p.IncidentPublicID))
	}
	if p.MonitorID != nil {
		add("Monitor", fmt.Sprint(*p.MonitorID))
	}
	add("Alert event", p.EventID)
	if p.EventTS != nil {
		add("Alert time", approvalTime(*p.EventTS))
	}
	add("Scope", strings.Join(p.Tags, ", "))
	if p.StartTime != nil && p.EndTime != nil {
		add("Time window", approvalTime(*p.StartTime)+" to "+approvalTime(*p.EndTime))
	}
	if investigation {
		lines = append(lines, "Progress and findings will appear in this conversation.")
	}
	var rows []string
	for _, line := range lines {
		rows = append(rows, textwrap.WrapProse(width, line, sty.ToolDetail)...)
	}
	return strings.Join(rows, "\n")
}

func approvalTime(milliseconds int64) string {
	return time.UnixMilli(milliseconds).UTC().Format("2006-01-02 15:04 UTC")
}

// highlightShellCommand returns the styled segments of each command line.
func highlightShellCommand(command string, sty Styles) [][]textwrap.Segment {
	return diffrender.Highlight("command.sh", command, sty.Diff.SyntaxDark, sty.ToolArgument)
}

func execResultIsTerminal(status spec.ExecStatus) bool {
	switch status {
	case spec.ExecSucceeded, spec.ExecNonZeroExit, spec.ExecLaunchFailed, spec.ExecTimedOut, spec.ExecCancelled:
		return true
	case spec.ExecRunning:
	}
	return false
}

func renderNoOutput(width int, sty Styles) string {
	return renderRows([]string{"(no output)"}, width, sty.ToolDetail, sty)
}

// renderExecInvocation keeps ordinary commands on the compact tool header.
// For a multiline command, the first source line stays in the header and the
// remaining lines form a bounded branch above the command output. This retains
// shell structure (especially heredocs) without allowing an arbitrary tool
// input to consume the transcript viewport. When expanded, every source line
// is shown.
func renderExecInvocation(tool *agent.ToolBlock, p toolPresentation, suffix []summarySpan, c renderContext) string {
	command := strings.TrimRight(p.argument, "\n")
	lines := highlightShellCommand(command, c.sty)
	if c.disclosure == fullView {
		return renderExecInvocationFull(tool, p, lines, suffix, c)
	}
	p.argument = textwrap.Render(lines[0]...)
	header := renderToolHeader(tool, p.summary(tool), suffix, c)
	rows := renderSourceBranch(lines[1:], execCommandMaxLines-1, textwrap.Shell, c)
	return strings.Join(append([]string{header}, rows...), "\n")
}

// renderSourceBranch lays out source lines behind a │ branch. The compact
// view truncates each line and middle-clamps them to limit rows. The full view
// shows every line wrapped, with soft-wrapped continuations indented further
// to keep them distinct from real newlines.
func renderSourceBranch(lines [][]textwrap.Segment, limit int, breakpoints string, c renderContext) []string {
	if c.disclosure == fullView {
		branch := textwrap.Options{
			Width:       c.width,
			FirstPrefix: c.sty.ToolDetail.Render("  │ "),
			RestPrefix:  c.sty.ToolDetail.Render("  │   "),
			Breakpoints: breakpoints,
			KeepIndent:  true,
		}
		var rows []string
		for _, line := range lines {
			rows = append(rows, textwrap.Wrap(branch, line...)...)
		}
		return rows
	}
	rows := make([]string, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, textwrap.Render(line...))
	}
	omissionRow := -1
	if len(rows) > limit {
		omissionRow = min(2, limit-1)
		rows = middleClamp(rows, limit)
	}
	for i, row := range rows {
		prefix := c.sty.ToolDetail.Render("  │ ")
		body := row
		if i == omissionRow {
			body = c.sty.ToolDetail.Render(row)
		}
		body = ansi.Truncate(body, max(1, c.width-4), "…")
		rows[i] = ansi.Truncate(prefix+body, max(1, c.width), "…")
	}
	return rows
}

// renderExecInvocationFull is the expanded invocation: every source line is
// shown in full, wrapped instead of truncated. The first line wraps under the
// command's first column, so expanding only adds rows below the compact
// header; later lines wrap behind the │ branch. Source indentation is kept on
// every row.
func renderExecInvocationFull(tool *agent.ToolBlock, p toolPresentation, lines [][]textwrap.Segment, suffix []summarySpan, c renderContext) string {
	state := statusOf(tool)
	action := p.actionLabel(state)
	lead := headerGlyph(state, c) + c.sty.ToolName.Render(action) + c.sty.ToolDetail.Render(" ")
	leadWidth := ansi.StringWidth(lead)

	meta := append(append(p.execMeta(), suffix...), lifecycleSpans(state)...)
	out := textwrap.Wrap(textwrap.Options{
		Width:       c.width,
		FirstPrefix: lead,
		RestPrefix:  strings.Repeat(" ", leadWidth),
		Breakpoints: textwrap.Shell,
		KeepIndent:  true,
	}, slices.Concat(lines[0], spanSegments(meta, c.sty))...)
	out = append(out, renderSourceBranch(lines[1:], 0, textwrap.Shell, c)...)
	return strings.Join(out, "\n")
}

func execSuffix(result *spec.ExecCommandOutput) []summarySpan {
	if result == nil {
		return nil
	}
	var text string
	switch result.Status {
	case spec.ExecRunning:
		text = " · running"
	case spec.ExecTimedOut:
		text = " · timed out"
	case spec.ExecLaunchFailed:
		text = " · could not start"
	default:
		if result.Signal != "" {
			text = " · signal " + escape.Inline(result.Signal)
		} else if result.Status == spec.ExecNonZeroExit && result.ExitCode != nil {
			text = fmt.Sprintf(" · exit %d", *result.ExitCode)
		}
	}
	if text == "" {
		return nil
	}
	return []summarySpan{{text: text, kind: spanMuted}}
}

// execRows lays out the command output, middle-clamped to execOutputMaxLines
// in the compact view.
func execRows(result spec.ExecCommandOutput, bodyWidth int, d disclosure) []string {
	stdout := strings.TrimRight(escape.Multiline(result.Stdout), "\n")
	stderr := strings.TrimRight(escape.Multiline(result.Stderr), "\n")
	if stderr == "" && result.Status == spec.ExecLaunchFailed {
		stderr = strings.TrimSpace(escape.Multiline(result.Error))
	}
	both := stdout != "" && stderr != ""
	rows := make([]string, 0, execOutputMaxLines+1)
	if stdout != "" {
		if both {
			stdout = "stdout: " + stdout
		}
		rows = append(rows, wrappedRows(stdout, bodyWidth)...)
	}
	if stderr != "" {
		if both {
			stderr = "stderr: " + stderr
		}
		rows = append(rows, wrappedRows(stderr, bodyWidth)...)
	}
	marker := ""
	switch {
	case result.Truncated && result.OutputIncomplete:
		marker = "… output truncated and incomplete"
	case result.Truncated:
		marker = "… output truncated"
	case result.OutputIncomplete:
		marker = "… output incomplete"
	}
	limit := execOutputMaxLines
	if marker != "" {
		limit--
	}
	if d == compactView {
		rows = middleClamp(rows, limit)
	}
	if marker != "" {
		rows = append(rows, ansi.Truncate(marker, bodyWidth, "…"))
	}
	return rows
}

// wrappedRows wraps plain multiline output, keeping each line's indentation
// and breaking paths at slashes.
func wrappedRows(s string, width int) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return textwrap.Wrap(textwrap.Options{Width: width, Breakpoints: textwrap.Output, KeepIndent: true}, textwrap.Plain(s))
}

func middleClamp(rows []string, limit int) []string {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	if limit == 1 {
		return []string{fmt.Sprintf("… +%d lines", len(rows))}
	}
	head := min(2, limit-1)
	tail := limit - head - 1
	omitted := len(rows) - head - tail
	out := append([]string(nil), rows[:head]...)
	out = append(out, fmt.Sprintf("… +%d lines", omitted))
	if tail > 0 {
		out = append(out, rows[len(rows)-tail:]...)
	}
	return out
}

// renderPreview renders text clamped to limit rows, or in full for fullView.
//
// TODO: bound very large expanded outputs if rendering cost shows up.
func renderPreview(text string, limit int, style lipgloss.Style, middle bool, c renderContext) string {
	text = strings.TrimRight(escape.Multiline(text), "\n")
	rows := wrappedRows(text, max(1, c.width-4))
	switch {
	case c.disclosure == fullView || len(rows) <= limit:
	case middle:
		rows = middleClamp(rows, limit)
	default:
		rows = append(append([]string(nil), rows[:limit-1]...), "…")
	}
	return renderRows(rows, c.width, style, c.sty)
}

// renderReason shows why a call failed on one row of its compact view.
func renderReason(reason string, c renderContext) string {
	return renderRows([]string{collapseWS(escape.Multiline(reason))}, c.width, c.sty.ToolError, c.sty)
}

// detailStyle styles a call's detail rows by its outcome.
func detailStyle(state agent.ToolStatus, sty Styles) lipgloss.Style {
	if state == agent.ToolError {
		return sty.ToolError
	}
	return sty.ToolDetail
}

func renderRows(rows []string, width int, style lipgloss.Style, sty Styles) string {
	for i := range rows {
		prefix := "    "
		if i == 0 {
			prefix = "  └ "
		}
		rows[i] = sty.ToolDetail.Render(prefix) + style.Render(ansi.Truncate(rows[i], max(1, width-4), "…"))
		rows[i] = ansi.Truncate(rows[i], width, "…")
	}
	return strings.Join(rows, "\n")
}

// statusGlyphWidth is the header's leading glyph plus its trailing space.
// Every status glyph (spinner frame, •, ✓, ✗) is one cell wide, so this is
// fixed rather than measured. List splices the accordion control after these
// two cells so the glyph stays the leftmost thing on the row regardless of
// disclosure state.
const statusGlyphWidth = 2

func renderToolHeader(tool *agent.ToolBlock, summary, suffix []summarySpan, c renderContext) string {
	state := statusOf(tool)
	spans := make([]summarySpan, 0, len(summary)+len(suffix)+1)
	spans = append(spans, summary...)
	spans = append(spans, suffix...)
	spans = append(spans, lifecycleSpans(state)...)
	header := headerGlyph(state, c) + renderSpans(spans, c.sty)
	return ansi.Truncate(header, max(1, c.width), "…")
}

func headerGlyph(state agent.ToolStatus, c renderContext) string {
	glyph, style := statusGlyph(state, c.sty, c.frame)
	return style.UnsetBackground().Render(glyph + " ")
}

func lifecycleSpans(state agent.ToolStatus) []summarySpan {
	if label := lifecycleSuffix(state); label != "" {
		return []summarySpan{{text: " · " + label, kind: spanMuted}}
	}
	return nil
}

func lifecycleSuffix(state agent.ToolStatus) string {
	switch state {
	case agent.ToolAwaitingApproval:
		return "awaiting approval"
	case agent.ToolDenied:
		return "denied"
	case agent.ToolCancelled:
		return "stopped"
	default:
		return ""
	}
}

func statusGlyph(state agent.ToolStatus, sty Styles, frame int) (string, lipgloss.Style) {
	switch state {
	case agent.ToolRunning:
		glyph := sty.StatusSpinner.Frame(frame)
		if glyph == "" {
			glyph = "•"
		}
		return glyph, sty.StatusRunning
	case agent.ToolAwaitingApproval:
		return "•", sty.StatusRunning
	case agent.ToolSuccess:
		return "✓", sty.StatusSuccess
	case agent.ToolError:
		return "✗", sty.StatusError
	default:
		return "•", sty.Meta
	}
}

// renderActivityHeader renders the shared compact header used by thinking and
// grouped inspection activity. Keeping the state-to-glyph mapping here makes
// every progressing row use the same spinner and static fallbacks.
func renderActivityHeader(state agent.ToolStatus, label, suffix string, c renderContext) string {
	glyph, glyphStyle := statusGlyph(state, c.sty, c.frame)
	header := glyphStyle.UnsetBackground().Render(glyph+" ") + c.sty.ToolName.Render(label)
	if suffix != "" {
		header += c.sty.ToolDetail.Render(suffix)
	}
	return ansi.Truncate(header, max(1, c.width), "…")
}

func renderSpans(spans []summarySpan, sty Styles) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(spanStyle(span.kind, sty).Render(span.text))
	}
	return b.String()
}

// spanSegments converts spans to segments for textwrap.
func spanSegments(spans []summarySpan, sty Styles) []textwrap.Segment {
	segs := make([]textwrap.Segment, len(spans))
	for i, span := range spans {
		segs[i] = textwrap.Styled(span.text, spanStyle(span.kind, sty))
	}
	return segs
}

func spanStyle(kind spanKind, sty Styles) lipgloss.Style {
	switch kind {
	case spanAction:
		return sty.ToolName
	case spanArgument:
		return sty.ToolArgument
	case spanError:
		return sty.ToolError
	default:
		return sty.ToolDetail
	}
}

// activityEllipsis returns a fixed-width, low-frequency progress indicator.
// The shared TUI clock ticks every 50ms; holding each step for eight frames
// makes the visible dots advance every 400ms without causing header reflow.
func activityEllipsis(frame int, motion bool) string {
	if !motion {
		return "..."
	}
	step := frame / 8 % 3
	if step < 0 {
		step += 3
	}
	dots := step + 1
	return fmt.Sprintf("%-3s", strings.Repeat(".", dots))
}

// count is n with the noun for its number, as in "1 call" or "2 calls".
func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// collapseWS joins whitespace runs.
func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

func renderQuestionsTool(tool *agent.ToolBlock, _ toolPresentation, c renderContext) string {
	state := statusOf(tool)
	label := "questions"
	if tool.Status == agent.ToolRunning && tool.HasFinalInput {
		state, label = agent.ToolAwaitingApproval, "waiting for your answers"
	}
	if tool.Status == agent.ToolCancelled {
		label = "questions cancelled"
	}
	header := renderActivityHeader(state, label, "", c)
	input, err := spec.ParseQuestions(tool.Input)
	if err != nil {
		return header + "\n" + textwrap.WrapProseText(c.width, escape.Multiline(tool.Output), c.sty.ToolError)
	}
	result, ok := decode[spec.AskUserQuestionOutput](tool.Output)
	if ok && result.Success {
		return header + "\n" + textwrap.WrapProseText(c.width, escape.Multiline(result.Message), c.sty.ToolDetail)
	}
	lines := []string{header}
	for _, question := range input.Questions {
		lines = append(lines, textwrap.WrapProseText(c.width, escape.Multiline("Q: "+question.Question), c.sty.ToolDetail))
	}
	if result.Message != "" {
		lines = append(lines, textwrap.WrapProseText(c.width, escape.Multiline(result.Message), c.sty.ToolDetail))
	} else if tool.Output != "" {
		lines = append(lines, textwrap.WrapProseText(c.width, escape.Multiline(tool.Output), c.sty.ToolDetail))
	}
	return strings.Join(lines, "\n")
}

// questionPrompt asks ask_user_question's questions with a Questionnaire.
type questionPrompt struct{ form *components.Questionnaire }

func newQuestionPrompt(call agent.ToolCall) ToolPrompt {
	input, err := spec.ParseQuestions(call.Input)
	if err != nil {
		return nil
	}
	questions := make([]components.Question, len(input.Questions))
	for i, question := range input.Questions {
		questions[i].Prompt = question.Question
		for _, option := range question.Options {
			questions[i].Options = append(questions[i].Options, components.Option{Label: option.Label, Description: option.Description})
		}
	}
	return questionPrompt{components.NewQuestionnaire(questions)}
}

func (q questionPrompt) Update(msg tea.Msg) (tea.Cmd, bool) { return q.form.Update(msg) }

func (q questionPrompt) Layout(slot components.Slot) (string, bool) {
	q.form.SetSize(slot.Width, slot.Height)
	minWidth, minHeight := q.form.MinSize()
	return q.form.View(), slot.Width >= minWidth && slot.Height >= minHeight
}

// Placement puts the form in place of the composer: answering it is the one
// thing to do.
func (q questionPrompt) Placement() components.Placement { return components.ReplacesInput }

func (q questionPrompt) SetStyles(theme styles.Theme) { q.form.SetStyles(theme) }

func (q questionPrompt) Result() (any, bool) {
	answers, dismissed, done := q.form.Answers()
	if !done {
		return nil, false
	}
	return spec.QuestionAnswers{Answers: answers, Dismissed: dismissed}, true
}

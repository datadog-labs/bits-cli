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
	inspectionGroupKey    = "Files"
	editGroupKey          = "Edits"
	skillGroupKey         = "Skills"
	serverGroupKey        = "Tools"
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
	// renderSpec is never nil: unknown tools use their client or server fallback.
	renderSpec *toolRenderSpec
}

// group returns the key that merges this call with adjacent calls sharing it,
// or "" when it stands alone. Local inspections require decodable input;
// server tools can group while their arguments are still streaming.
func (p toolPresentation) group(tool *agent.ToolBlock) string {
	if tool.Status == agent.ToolAwaitingApproval || tool.Approval != nil {
		return ""
	}
	if !p.validInput {
		return ""
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
	// header; "" keeps each call on its own.
	group string
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
	serverToolRenderSpec       = &toolRenderSpec{render: renderServerTool, group: serverGroupKey}
	questionToolRenderSpec     = &toolRenderSpec{render: renderQuestionsTool, spacing: itemSpacing{before: 1, after: 1}, static: true, interact: newQuestionPrompt}
	simpleToolRenderSpec       = &toolRenderSpec{render: renderSimpleTool}
	spawnAgentRenderSpec       = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Start subagent", active: "Starting subagent", success: "Started subagent", failure: "Subagent start failed"}}
	sendAgentMessageRenderSpec = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Send message", active: "Sending message", success: "Sent message", failure: "Message failed"}}
	waitForAgentsRenderSpec    = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Wait for", active: "Waiting for", success: "Waited for", failure: "Wait failed"}}
	readToolRenderSpec         = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Read", active: "Reading"}, group: inspectionGroupKey}
	listToolRenderSpec         = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "List", active: "Listing"}, group: inspectionGroupKey}
	grepToolRenderSpec         = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "Search", active: "Searching"}, group: inspectionGroupKey}
	writeToolRenderSpec        = &toolRenderSpec{
		render:  renderChangeTool,
		group:   editGroupKey,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "Write", active: "Writing", success: "Wrote", failure: "Write failed"},
	}
	editToolRenderSpec = &toolRenderSpec{
		render:  renderChangeTool,
		group:   editGroupKey,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "Edit", active: "Editing", success: "Edited", failure: "Edit failed"},
	}
	execToolRenderSpec = &toolRenderSpec{
		render:   renderExecTool,
		approval: &toolApprovalSpec{render: renderExecApproval},
		spacing:  itemSpacing{before: 1, after: 1},
		action:   toolAction{base: "Run", success: "Ran", failure: "Run failed"},
	}
	skillToolRenderSpec = &toolRenderSpec{
		render: renderSimpleTool,
		group:  skillGroupKey,
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
	case agent.ToolRunning, agent.ToolAwaitingApproval:
		label = a.active
	case agent.ToolSuccess:
		label = a.success
	case agent.ToolError:
		label = a.failure
	case agent.ToolUnknown, agent.ToolDenied, agent.ToolCancelled:
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
// with names matching local tools deliberately remain generic.
//
// TODO: Presentation input decoding is repeated when the transcript view
// rebuilds.
func classifyTool(tool *agent.ToolBlock) toolPresentation {
	if tool == nil {
		return toolPresentation{renderSpec: simpleToolRenderSpec}
	}
	qualified := qualifiedToolName(tool)
	input := toolInput(tool)
	id := spec.Identity{ClientSide: tool.IsClientSide, Namespace: toolNamespace(tool), Name: tool.Name}
	p := toolPresentation{identity: id, name: qualified, renderSpec: toolRenderSpecFor(id)}

	switch id {
	case spec.ClientReadFile, spec.ClientWriteFile, spec.ClientEditFile:
		var in spec.PathInput
		if decodeObject(input, &in) && in.Path != "" {
			p.argument, p.validInput = escape.Inline(in.Path), true
		}
	case spec.ClientListFiles:
		var in spec.ListFilesInput
		if decodeObject(input, &in) {
			if in.Path == "" {
				in.Path = "."
			}
			p.argument, p.validInput = escape.Inline(in.Path), true
			if in.Depth != nil {
				p.context = fmt.Sprintf("depth %d", *in.Depth)
			}
		}
	case spec.ClientGrepFiles:
		var in spec.GrepFilesInput
		if decodeObject(input, &in) && in.Pattern != "" {
			p.argument, p.context = escape.Inline(in.Pattern), escape.Inline(in.Path)
			p.validInput = true
		}
	case spec.ClientExecCommand:
		var in spec.ExecCommandInput
		if decodeObject(input, &in) && in.Cmd != "" {
			// Preserve command line boundaries for the exec-specific renderer. The
			// working directory remains compact metadata in the header.
			if in.Workdir == "" {
				in.Workdir = "."
			}
			p.argument, p.context = escape.Multiline(in.Cmd), escape.SingleLine(in.Workdir)
			if in.TimeoutMS != nil && *in.TimeoutMS > 0 && *in.TimeoutMS <= spec.ExecMaxTimeoutMS {
				p.timeout = (time.Duration(*in.TimeoutMS) * time.Millisecond).String()
			}
			p.validInput = true
		}
	case spec.ServerSpawnAgent:
		var in struct {
			Prompt string `json:"prompt"`
		}
		if decodeObject(input, &in) && in.Prompt != "" {
			p.argument, p.validInput = escape.Inline(in.Prompt), true
		}
	case spec.ServerSendAgentMessage:
		var in struct {
			Message string `json:"message"`
		}
		if decodeObject(input, &in) && in.Message != "" {
			p.argument, p.validInput = escape.Inline(in.Message), true
		}
	case spec.ServerWaitForAgents:
		var in struct {
			AgentIDs []string `json:"agent_ids"`
			Mode     string   `json:"mode"`
		}
		if decodeObject(input, &in) && len(in.AgentIDs) > 0 {
			p.argument = fmt.Sprintf("%d subagents", len(in.AgentIDs))
			if len(in.AgentIDs) == 1 {
				p.argument = "1 subagent"
			}
			if in.Mode == "any" && len(in.AgentIDs) > 1 {
				p.argument = "any of " + p.argument
			}
			p.validInput = true
		}
	case spec.ServerSkill:
		p.argument = "skill"
		var in spec.SkillInput
		if decodeObject(input, &in) && in.SkillName != "" {
			p.argument = "skill " + escape.Inline(in.SkillName)
		}
		p.validInput = true
	case spec.ServerStartBackgroundTask:
		if in, ok := backgroundTaskInput(input); ok {
			p.argument, p.validInput = escape.Inline(cmp.Or(in.Task.TaskName, backgroundAgentName(in.Task.AgentID))), true
		}
	case spec.ClientHandoff:
		var in struct {
			Reason string `json:"reason"`
		}
		if decodeObject(input, &in) && in.Reason != "" {
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
	default:
		if !id.ClientSide {
			return serverToolRenderSpec
		}
		return simpleToolRenderSpec
	}
}

func decodeObject(input string, out any) bool {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "{") {
		return false
	}
	return json.Unmarshal([]byte(input), out) == nil
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
	if namespace := toolNamespace(tool); namespace != "" {
		name = namespace + "." + name
	}
	return escape.Inline(name)
}

func toolNamespace(tool *agent.ToolBlock) string {
	if tool == nil || tool.Namespace == nil {
		return ""
	}
	return *tool.Namespace
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
	return p.renderSpec.action.label(state)
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

func renderPresentedTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	return p.renderSpec.render(tool, p, c)
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

// approvalSubject returns the server call tool's approval prompt gates, as a
// tool block awaiting approval, or tool itself.
func approvalSubject(tool *agent.ToolBlock) *agent.ToolBlock {
	if tool == nil || tool.Approval == nil || tool.Approval.Gated == nil {
		return tool
	}
	gated := tool.Approval.Gated
	return &agent.ToolBlock{Name: gated.Name, Input: gated.Input, HasFinalInput: true, Status: tool.Status, Approval: tool.Approval}
}

// renderToolGroupHeader counts tool calls, while reasoning only contributes
// to the running state. Children always remain visible beneath this header.
func renderToolGroupHeader(blocks []agent.Block, presentations []toolPresentation, c renderContext) string {
	family := presentations[0].group(blocks[0].Tool)
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
		if group := p.group(block.Tool); group != skillGroupKey {
			family = group
		}
		if p.identity == spec.ServerSkill {
			skills++
		}
		if p.renderSpec.group == editGroupKey {
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
	noun := "calls"
	if calls == 1 {
		noun = "call"
	}
	suffix := fmt.Sprintf(" · %d %s", calls, noun)
	if family == editGroupKey {
		operations, paths := "changes", "files"
		if changes == 1 {
			operations = "change"
		}
		if len(files) == 1 {
			paths = "file"
		}
		suffix = fmt.Sprintf(" · %d %s across %d %s", changes, operations, len(files), paths)
		if skills > 0 {
			noun := "skills"
			if skills == 1 {
				noun = "skill"
			}
			suffix += fmt.Sprintf(" · %d %s", skills, noun)
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
	title := family
	if family == serverGroupKey {
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
		return header + "\n" + renderRows([]string{collapseWS(escape.Multiline(reason))}, c.width, c.sty.ToolError, c.sty)
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
	style := c.sty.ToolDetail
	if statusOf(tool) == agent.ToolError {
		style = c.sty.ToolError
	}
	return header + "\n" + renderPreview(strings.Join(sections, "\n\n"), genericOutputMaxLines, style, false, c)
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
			return header + "\n" + renderRows([]string{collapseWS(escape.Multiline(reason))}, c.width, c.sty.ToolError, c.sty)
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
	style := c.sty.ToolDetail
	if state == agent.ToolError {
		style = c.sty.ToolError
	}
	return renderPreview(detail, limit, style, false, c)
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
	var result spec.ExecCommandOutput
	decoded := json.Unmarshal([]byte(tool.Output), &result) == nil
	var suffix []summarySpan
	if decoded {
		suffix = execSuffix(result)
	}
	header := renderExecInvocation(tool, p, suffix, c)
	state := statusOf(tool)
	if tool.Output == "" {
		return header
	}
	switch state {
	case agent.ToolRunning, agent.ToolAwaitingApproval, agent.ToolDenied:
		return header
	case agent.ToolCancelled:
		if decoded && execResultIsTerminal(result.Status) && len(execRows(result, max(1, c.width-4), compactView)) == 0 {
			return header + "\n" + renderNoOutput(c.width, c.sty)
		}
		return header
	case agent.ToolUnknown, agent.ToolSuccess, agent.ToolError:
	}
	style := c.sty.ToolDetail
	if state == agent.ToolError {
		style = c.sty.ToolError
	}
	if !decoded {
		return header + "\n" + renderPreview(tool.Output, execOutputMaxLines, style, true, c)
	}
	rows := execRows(result, max(1, c.width-4), c.disclosure)
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

// backgroundTaskInput decodes a startBackgroundTask call's task.
func backgroundTaskInput(input string) (spec.StartBackgroundTaskInput, bool) {
	var in spec.StartBackgroundTaskInput
	return in, decodeObject(input, &in) && strings.TrimSpace(in.Task.AgentID) != ""
}

func backgroundAgentName(agentID string) string {
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

func execResultIsTerminal(status spec.ExecTerminalReason) bool {
	switch status {
	case spec.ExecSucceeded, spec.ExecNonZeroExit, spec.ExecLaunchFailed, spec.ExecTimedOut, spec.ExecCancelled:
		return true
	default:
		return false
	}
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
	if len(lines) == 1 {
		return header
	}

	rows := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		rows = append(rows, textwrap.Render(line...))
	}
	limit := execCommandMaxLines - 1
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
	return header + "\n" + strings.Join(rows, "\n")
}

// renderExecInvocationFull is the expanded invocation: every source line is
// shown in full, wrapped instead of truncated. The first line wraps under the
// command's first column, so expanding only adds rows below the compact
// header; later lines wrap behind the │ branch, with soft-wrapped
// continuations indented further to keep them distinct from real newlines.
// Source indentation is kept on every row.
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
	branch := textwrap.Options{
		Width:       c.width,
		FirstPrefix: c.sty.ToolDetail.Render("  │ "),
		RestPrefix:  c.sty.ToolDetail.Render("  │   "),
		Breakpoints: textwrap.Shell,
		KeepIndent:  true,
	}
	for _, row := range lines[1:] {
		out = append(out, textwrap.Wrap(branch, row...)...)
	}
	return strings.Join(out, "\n")
}

func execSuffix(result spec.ExecCommandOutput) []summarySpan {
	var text string
	switch result.Status {
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
	var result spec.AskUserQuestionOutput
	if json.Unmarshal([]byte(tool.Output), &result) == nil && result.Success {
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

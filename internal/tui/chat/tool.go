package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/datadog-labs/bits-cli/internal/agent"
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
	inspectionGroupKey    = "inspect"
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
	// renderSpec is never nil: unknown tools use simpleToolRenderSpec.
	renderSpec *toolRenderSpec
}

// group returns the key that merges this call with adjacent calls sharing it,
// or "" when it stands alone. Only decodable input groups, so a malformed
// call stays visible as its own row.
func (p toolPresentation) group() string {
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
	render         toolRenderFunc
	renderApproval toolApprovalRenderFunc
	spacing        itemSpacing
	action         toolAction
	// group merges adjacent calls with the same key into one presentation
	// item; "" keeps each call on its own.
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
	questionToolRenderSpec = &toolRenderSpec{render: renderQuestionsTool, spacing: itemSpacing{before: 1, after: 1}, static: true, interact: newQuestionPrompt}
	simpleToolRenderSpec   = &toolRenderSpec{render: renderSimpleTool}
	readToolRenderSpec     = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "read", active: "reading"}, group: inspectionGroupKey}
	listToolRenderSpec     = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "list", active: "listing"}, group: inspectionGroupKey}
	grepToolRenderSpec     = &toolRenderSpec{render: renderSimpleTool, action: toolAction{base: "search", active: "searching"}, group: inspectionGroupKey}
	writeToolRenderSpec    = &toolRenderSpec{
		render:  renderChangeTool,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "write", active: "writing", success: "wrote", failure: "write failed"},
	}
	editToolRenderSpec = &toolRenderSpec{
		render:  renderChangeTool,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "edit", active: "editing", success: "edited", failure: "edit failed"},
	}
	execToolRenderSpec = &toolRenderSpec{
		render:         renderExecTool,
		renderApproval: renderExecApproval,
		spacing:        itemSpacing{before: 1, after: 1},
		action:         toolAction{base: "run", success: "ran", failure: "run failed"},
	}
	skillToolRenderSpec = &toolRenderSpec{
		render:  renderSimpleTool,
		spacing: itemSpacing{before: 1, after: 1},
		action:  toolAction{base: "load", active: "loading", success: "loaded", failure: "load failed"},
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
			p.argument, p.context = escape.Multiline(in.Cmd), escape.SingleLine(in.Workdir)
			if in.TimeoutMS != nil && *in.TimeoutMS > 0 && *in.TimeoutMS <= spec.ExecMaxTimeoutMS {
				p.timeout = (time.Duration(*in.TimeoutMS) * time.Millisecond).String()
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
	case spec.ServerSkill:
		return skillToolRenderSpec
	default:
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
		// Keep local inspection tools recognizable while their input is still
		// streaming or malformed. Exact client identity is enough to choose the
		// action; arguments remain omitted until decoding succeeds. Server tools
		// stay generic so similarly named remote tools are not specialized.
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

// RenderToolApproval renders the tool-specific portion of an approval prompt.
// The caller retains ownership of the surrounding panel, title, and actions.
// Tools without a specialized approval design use the caller's generic fallback.
func RenderToolApproval(tool *agent.ToolBlock, width int, sty Styles) (string, bool) {
	p := classifyTool(tool)
	if p.renderSpec.renderApproval == nil || !p.validInput {
		return "", false
	}
	return p.renderSpec.renderApproval(tool, p, max(1, width), sty), true
}

// renderSimpleTool shows only the header until expanded. The full view adds
// the whole detail or output, or "(no output)" once the call has settled.
func renderSimpleTool(tool *agent.ToolBlock, p toolPresentation, c renderContext) string {
	header := renderToolHeader(tool, p.summary(tool), nil, c)
	if c.disclosure == compactView {
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
		rows = append(rows, ansi.Hardwrap(textwrap.Render(line), width, true))
	}

	detail := ""
	if tool.Approval != nil {
		detail = escape.Inline(tool.Approval.Detail)
	}
	if detail != "" {
		rows = append(rows, sty.ToolDetail.Render(ansi.Wordwrap(detail, width, "-")))
	}
	return strings.Join(rows, "\n")
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
	p.argument = textwrap.Render(lines[0])
	header := renderToolHeader(tool, p.summary(tool), suffix, c)
	if len(lines) == 1 {
		return header
	}

	rows := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		rows = append(rows, textwrap.Render(line))
	}
	limit := execCommandMaxLines - 1
	omissionRow := -1
	if len(rows) > limit && c.disclosure == compactView {
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
	leadWidth := statusGlyphWidth + ansi.StringWidth(action) + 1

	meta := append(append(p.execMeta(), suffix...), lifecycleSpans(state)...)
	out := textwrap.Wrap(slices.Concat(lines[0], spanSegments(meta, c.sty)), textwrap.Options{
		Width:       c.width,
		FirstPrefix: lead,
		RestPrefix:  strings.Repeat(" ", leadWidth),
		Breakpoints: shellBreakpoints,
		KeepIndent:  true,
	})
	branch := textwrap.Options{
		Width:       c.width,
		FirstPrefix: c.sty.ToolDetail.Render("  │ "),
		RestPrefix:  c.sty.ToolDetail.Render("  │   "),
		Breakpoints: shellBreakpoints,
		KeepIndent:  true,
	}
	for _, row := range lines[1:] {
		out = append(out, textwrap.Wrap(row, branch)...)
	}
	return strings.Join(truncateRows(out, c.width), "\n")
}

// shellBreakpoints are extra word-wrap opportunities in shell source. "&" is
// left out: splitting "&&" helps no one.
const shellBreakpoints = "/|;"

// truncateRows cuts rows wider than width, which wrapping only leaves when a
// prefix alone does not fit.
func truncateRows(rows []string, width int) []string {
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], max(1, width), "…")
	}
	return rows
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

func wrappedRows(s string, width int) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(wrap(s, max(1, width)), "\n")
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

// inspectionLifecycle is the group's activity: running or awaiting approval
// while any call is, then success once any call succeeded. Each call reports
// its own failure, so a settled group without a success stays neutral rather
// than alarming: inspection failures are routine.
func inspectionLifecycle(blocks []agent.Block) agent.ToolStatus {
	state := agent.ToolUnknown
	for i := range blocks {
		switch statusOf(blocks[i].Tool) {
		case agent.ToolRunning:
			return agent.ToolRunning
		case agent.ToolAwaitingApproval:
			state = agent.ToolAwaitingApproval
		case agent.ToolSuccess:
			if state != agent.ToolAwaitingApproval {
				state = agent.ToolSuccess
			}
		case agent.ToolUnknown, agent.ToolError, agent.ToolDenied, agent.ToolCancelled:
		}
	}
	return state
}

func renderInspectionGroup(blocks []agent.Block, presentations []toolPresentation, c renderContext) string {
	state := inspectionLifecycle(blocks)
	label, suffix := "inspected", ""
	switch state {
	case agent.ToolRunning:
		label, suffix = "inspecting", activityEllipsis(c.frame, c.sty.StatusSpinner.Len() > 0)
	case agent.ToolAwaitingApproval:
		label, suffix = "inspecting", " · awaiting approval"
	case agent.ToolUnknown, agent.ToolSuccess, agent.ToolError, agent.ToolDenied, agent.ToolCancelled:
	}
	lines := []string{renderActivityHeader(state, label, suffix, c)}

	first := true
	for i := 0; i < len(blocks); {
		p := presentations[i]
		memberState := statusOf(blocks[i].Tool)
		if p.identity == spec.ClientReadFile && (memberState == agent.ToolRunning || memberState == agent.ToolSuccess) {
			paths := make([]string, 0, 2)
			seen := make(map[string]struct{})
			j := i
			for j < len(blocks) && presentations[j].identity == spec.ClientReadFile {
				s := statusOf(blocks[j].Tool)
				if s != agent.ToolRunning && s != agent.ToolSuccess {
					break
				}
				path := presentations[j].argument
				if _, ok := seen[path]; !ok {
					seen[path] = struct{}{}
					paths = append(paths, path)
				}
				j++
			}
			spans := []summarySpan{{text: "read", kind: spanAction}, {text: " ", kind: spanMuted}}
			for n, path := range paths {
				if n > 0 {
					spans = append(spans, summarySpan{text: ", ", kind: spanMuted})
				}
				spans = append(spans, summarySpan{text: path, kind: spanArgument})
			}
			lines = append(lines, renderInspectionChild(spans, nil, first, c.width, c.sty)...)
			first, i = false, j
			continue
		}
		lines = append(lines, renderInspectionChild(p.summary(blocks[i].Tool), inspectionOutcome(blocks[i].Tool), first, c.width, c.sty)...)
		first, i = false, i+1
	}
	return strings.Join(lines, "\n")
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

// inspectionOutcome is the suffix a group row adds for a call that did not
// succeed: its error on one line, or the denied/stopped lifecycle label.
func inspectionOutcome(tool *agent.ToolBlock) []summarySpan {
	switch state := statusOf(tool); state {
	case agent.ToolError:
		detail := tool.Detail
		if detail == "" {
			detail = tool.Output
		}
		if detail = collapseWS(escape.Multiline(detail)); detail == "" {
			detail = "failed"
		}
		return []summarySpan{{text: " (" + detail + ")", kind: spanError}}
	case agent.ToolDenied, agent.ToolCancelled:
		return []summarySpan{{text: " · " + lifecycleSuffix(state), kind: spanMuted}}
	case agent.ToolUnknown, agent.ToolRunning, agent.ToolAwaitingApproval, agent.ToolSuccess:
	}
	return nil
}

// renderInspectionChild renders one group row. The summary wraps with a
// hanging indent; a row with an outcome stays on one line, cut at the width,
// so failures never grow the group.
func renderInspectionChild(spans, outcome []summarySpan, first bool, width int, sty Styles) []string {
	prefix := "    "
	if first {
		prefix = "  └ "
	}
	if len(spans) == 0 {
		return []string{sty.ToolDetail.Render(prefix)}
	}
	action := spans[0]
	if action.kind != spanAction || len(outcome) > 0 {
		spans = append(append([]summarySpan(nil), spans...), outcome...)
		line := sty.ToolDetail.Render(prefix) + renderSpans(spans, sty)
		return []string{ansi.Truncate(line, max(1, width), "…")}
	}
	rest := append([]summarySpan(nil), spans[1:]...)
	if len(rest) > 0 && rest[0].kind == spanMuted {
		rest[0].text = strings.TrimPrefix(rest[0].text, " ")
		if rest[0].text == "" {
			rest = rest[1:]
		}
	}
	actionRendered := sty.ToolName.Render(action.text)
	if len(rest) == 0 {
		line := sty.ToolDetail.Render(prefix) + actionRendered
		return []string{ansi.Truncate(line, max(1, width), "…")}
	}
	indentWidth := ansi.StringWidth(prefix) + ansi.StringWidth(action.text) + 1
	if width-indentWidth < 1 {
		line := sty.ToolDetail.Render(prefix) + actionRendered + sty.ToolDetail.Render(" ") + renderSpans(rest, sty)
		return []string{ansi.Truncate(line, max(1, width), "…")}
	}
	rows := textwrap.Wrap(spanSegments(rest, sty), textwrap.Options{
		Width:       width,
		FirstPrefix: sty.ToolDetail.Render(prefix) + actionRendered + sty.ToolDetail.Render(" "),
		RestPrefix:  sty.ToolDetail.Render(strings.Repeat(" ", indentWidth)),
		Breakpoints: "-",
		Ellipsis:    "…",
	})
	return truncateRows(rows, width)
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
		return header + "\n" + c.sty.ToolError.Render(wrap(escape.Multiline(tool.Output), c.width))
	}
	var result spec.AskUserQuestionOutput
	if json.Unmarshal([]byte(tool.Output), &result) == nil && result.Success {
		return header + "\n" + c.sty.ToolDetail.Render(wrap(escape.Multiline(result.Message), c.width))
	}
	lines := []string{header}
	for _, question := range input.Questions {
		lines = append(lines, c.sty.ToolDetail.Render(wrap(escape.Multiline("Q: "+question.Question), c.width)))
	}
	if result.Message != "" {
		lines = append(lines, c.sty.ToolDetail.Render(wrap(escape.Multiline(result.Message), c.width)))
	} else if tool.Output != "" {
		lines = append(lines, c.sty.ToolDetail.Render(wrap(escape.Multiline(tool.Output), c.width)))
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

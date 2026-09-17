package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/filediff"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/tui/diffrender"
	"github.com/DataDog/bits-cli/internal/tui/escape"
)

const (
	genericOutputMaxLines = 3
	execCommandMaxLines   = 10
	execOutputMaxLines    = 5
	collapsedDiffLines    = 8
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
	group      string
	validInput bool
}

type summarySpan struct {
	text string
	kind spanKind
}

type spanKind uint8

const (
	spanAction spanKind = iota
	spanArgument
	spanMuted
)

type toolLifecycle uint8

const (
	lifecycleUnknown toolLifecycle = iota
	lifecycleRunning
	lifecycleAwaiting
	lifecycleSuccess
	lifecycleError
	lifecycleDenied
	lifecycleCancelled
)

func lifecycleOf(tool *agent.ToolBlock) toolLifecycle {
	if tool == nil {
		return lifecycleUnknown
	}
	if tool.Denied {
		return lifecycleDenied
	}
	if tool.Cancelled {
		return lifecycleCancelled
	}
	switch tool.Status {
	case agent.ToolRunning:
		return lifecycleRunning
	case agent.ToolAwaitingApproval:
		return lifecycleAwaiting
	case agent.ToolSuccess:
		return lifecycleSuccess
	case agent.ToolError:
		return lifecycleError
	default:
		return lifecycleUnknown
	}
}

// classifyTool uses the complete wire identity. In particular, server tools
// with names matching local tools deliberately remain generic.
//
// TODO: Presentation input decoding is repeated when the transcript view
// rebuilds.
func classifyTool(tool *agent.ToolBlock) toolPresentation {
	if tool == nil {
		return toolPresentation{}
	}
	qualified := qualifiedToolName(tool)
	input := toolInput(tool)
	id := spec.Identity{ClientSide: tool.IsClientSide, Namespace: toolNamespace(tool), Name: tool.Name}
	p := toolPresentation{identity: id, name: qualified}

	switch id {
	case spec.ClientReadFile, spec.ClientWriteFile, spec.ClientEditFile:
		var in spec.PathInput
		if decodeObject(input, &in) && in.Path != "" {
			p.argument, p.validInput = escape.Inline(in.Path), true
			if id == spec.ClientReadFile {
				p.group = inspectionGroupKey
			}
		}
	case spec.ClientListFiles:
		var in spec.PathInput
		if decodeObject(input, &in) {
			if in.Path == "" {
				in.Path = "."
			}
			p.argument, p.group, p.validInput = escape.Inline(in.Path), inspectionGroupKey, true
		}
	case spec.ClientGrepFiles:
		var in spec.GrepFilesInput
		if decodeObject(input, &in) && in.Pattern != "" {
			p.argument, p.context = escape.Inline(in.Pattern), escape.Inline(in.Path)
			p.group, p.validInput = inspectionGroupKey, true
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
	default:
		p.argument, p.validInput = compactInput(input), true
	}
	return p
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
	state := lifecycleOf(tool)
	if !p.validInput {
		return []summarySpan{{text: p.name, kind: spanAction}}
	}
	switch p.identity {
	case spec.ClientReadFile:
		return actionArgument("read", p.argument)
	case spec.ClientListFiles:
		return actionArgument("list", p.argument)
	case spec.ClientGrepFiles:
		spans := actionArgument("search", p.argument)
		if p.context != "" && p.context != "." {
			spans = append(spans, summarySpan{text: " in ", kind: spanMuted}, summarySpan{text: p.context, kind: spanArgument})
		}
		return spans
	case spec.ClientWriteFile:
		action := "write"
		switch state {
		case lifecycleRunning, lifecycleAwaiting:
			action = "writing"
		case lifecycleSuccess:
			action = "wrote"
		case lifecycleError:
			action = "write failed"
		case lifecycleUnknown, lifecycleDenied, lifecycleCancelled:
		}
		return actionArgument(action, p.argument)
	case spec.ClientEditFile:
		action := "edit"
		switch state {
		case lifecycleRunning, lifecycleAwaiting:
			action = "editing"
		case lifecycleSuccess:
			action = "edited"
		case lifecycleError:
			action = "edit failed"
		case lifecycleUnknown, lifecycleDenied, lifecycleCancelled:
		}
		return actionArgument(action, p.argument)
	case spec.ClientExecCommand:
		action := "run"
		switch state {
		case lifecycleRunning, lifecycleAwaiting:
		case lifecycleSuccess:
			action = "ran"
		case lifecycleError:
			action = "run failed"
		case lifecycleUnknown, lifecycleDenied, lifecycleCancelled:
		}
		spans := actionArgument(action, p.argument)
		if p.context != "" {
			spans = append(spans, summarySpan{text: " in ", kind: spanMuted}, summarySpan{text: p.context, kind: spanArgument})
		}
		if p.timeout != "" {
			spans = append(spans, summarySpan{text: " · timeout ", kind: spanMuted}, summarySpan{text: p.timeout, kind: spanArgument})
		}
		return spans
	default:
		return []summarySpan{{text: p.name, kind: spanAction}, {text: "(" + p.argument + ")", kind: spanArgument}}
	}
}

func actionArgument(action, argument string) []summarySpan {
	spans := []summarySpan{{text: action, kind: spanAction}}
	if argument != "" {
		spans = append(spans, summarySpan{text: " ", kind: spanMuted}, summarySpan{text: argument, kind: spanArgument})
	}
	return spans
}

// renderTool renders an individual call. Inspection grouping is performed by
// List before this point; RenderBlock intentionally remains a one-block API.
func renderTool(it agent.Block, width int, sty Styles, frame int) string {
	if it.Tool == nil {
		return fallback(it, width, sty)
	}
	return renderPresentedTool(it.Tool, classifyTool(it.Tool), width, sty, frame)
}

func renderPresentedTool(tool *agent.ToolBlock, p toolPresentation, width int, sty Styles, frame int) string {
	switch p.identity {
	case spec.ClientWriteFile, spec.ClientEditFile:
		return renderChangeTool(tool, p, width, sty, frame)
	case spec.ClientExecCommand:
		return renderExecTool(tool, p, width, sty, frame)
	default:
		return renderSimpleTool(tool, p, width, sty, frame)
	}
}

func renderSimpleTool(tool *agent.ToolBlock, p toolPresentation, width int, sty Styles, frame int) string {
	state := lifecycleOf(tool)
	header := renderToolHeader(tool, p.summary(tool), nil, width, sty, frame)
	switch state {
	case lifecycleRunning, lifecycleAwaiting, lifecycleDenied, lifecycleCancelled:
		return header
	case lifecycleUnknown, lifecycleSuccess, lifecycleError:
	}
	if p.inspection() && state == lifecycleSuccess {
		return header
	}
	detail := tool.Detail
	if detail == "" {
		detail = tool.Output
	}
	if detail == "" {
		return header
	}
	style := sty.ToolDetail
	if state == lifecycleError {
		style = sty.ToolError
	}
	return header + "\n" + renderPreview(detail, width, genericOutputMaxLines, style, false, sty)
}

func (p toolPresentation) inspection() bool {
	switch p.identity {
	case spec.ClientReadFile, spec.ClientListFiles, spec.ClientGrepFiles:
		return true
	default:
		return false
	}
}

func renderChangeTool(tool *agent.ToolBlock, p toolPresentation, width int, sty Styles, frame int) string {
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
	header := renderToolHeader(tool, p.summary(tool), nil, width, sty, frame)
	if diff != nil {
		lines := []string{header}
		if format := formatChange(*diff); format != "" {
			lines = append(lines, sty.ToolDetail.Render(ansi.Truncate("  └ "+format, width, "…")))
		}
		body := diffrender.Render(*diff, diffrender.Options{Path: path, Width: width, Style: sty.Diff, MaxLines: collapsedDiffLines, Tail: true})
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
	state := lifecycleOf(tool)
	if fallbackDetail == "" {
		return header
	}
	switch state {
	case lifecycleRunning, lifecycleDenied, lifecycleCancelled:
		return header
	case lifecycleUnknown, lifecycleAwaiting, lifecycleSuccess, lifecycleError:
	}
	style := sty.ToolDetail
	if state == lifecycleError {
		style = sty.ToolError
	}
	return header + "\n" + renderPreview(fallbackDetail, width, genericOutputMaxLines, style, false, sty)
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

func renderExecTool(tool *agent.ToolBlock, p toolPresentation, width int, sty Styles, frame int) string {
	var result spec.ExecCommandOutput
	decoded := json.Unmarshal([]byte(tool.Output), &result) == nil
	var suffix []summarySpan
	if decoded {
		suffix = execSuffix(result)
	}
	header := renderExecInvocation(tool, p, suffix, width, sty, frame)
	state := lifecycleOf(tool)
	if tool.Output == "" {
		return header
	}
	switch state {
	case lifecycleRunning, lifecycleAwaiting, lifecycleDenied, lifecycleCancelled:
		return header
	case lifecycleUnknown, lifecycleSuccess, lifecycleError:
	}
	style := sty.ToolDetail
	if state == lifecycleError {
		style = sty.ToolError
	}
	if !decoded {
		return header + "\n" + renderPreview(tool.Output, width, execOutputMaxLines, style, true, sty)
	}
	rows := execRows(result, max(1, width-4))
	if len(rows) == 0 {
		return header
	}
	return header + "\n" + renderRows(rows, width, style, sty)
}

// renderExecInvocation keeps ordinary commands on the compact tool header.
// For a multiline command, the first source line stays in the header and the
// remaining lines form a bounded branch above the command output. This retains
// shell structure (especially heredocs) without allowing an arbitrary tool
// input to consume the transcript viewport.
func renderExecInvocation(tool *agent.ToolBlock, p toolPresentation, suffix []summarySpan, width int, sty Styles, frame int) string {
	command := strings.TrimRight(p.argument, "\n")
	const shellPath = "command.sh"
	lines := diffrender.HighlightLines(shellPath, command, sty.Diff.SyntaxDark, sty.ToolArgument)
	p.argument = lines[0]
	header := renderToolHeader(tool, p.summary(tool), suffix, width, sty, frame)
	if len(lines) == 1 {
		return header
	}

	sourceRows := lines[1:]
	limit := execCommandMaxLines - 1
	omissionRow := -1
	if len(sourceRows) > limit {
		omissionRow = min(2, limit-1)
	}
	rows := middleClamp(sourceRows, limit)
	for i, row := range rows {
		prefix := sty.ToolDetail.Render("  │ ")
		body := row
		if i == omissionRow {
			body = sty.ToolDetail.Render(row)
		}
		body = ansi.Truncate(body, max(1, width-4), "…")
		rows[i] = ansi.Truncate(prefix+body, max(1, width), "…")
	}
	return header + "\n" + strings.Join(rows, "\n")
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

func execRows(result spec.ExecCommandOutput, bodyWidth int) []string {
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
	rows = middleClamp(rows, limit)
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

func renderPreview(text string, width, limit int, style lipgloss.Style, middle bool, sty Styles) string {
	text = strings.TrimRight(escape.Multiline(text), "\n")
	rows := wrappedRows(text, max(1, width-4))
	if middle {
		rows = middleClamp(rows, limit)
	} else if len(rows) > limit {
		rows = append(append([]string(nil), rows[:limit-1]...), "…")
	}
	return renderRows(rows, width, style, sty)
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

func renderToolHeader(tool *agent.ToolBlock, summary, suffix []summarySpan, width int, sty Styles, frame int) string {
	state := lifecycleOf(tool)
	glyph, glyphStyle := statusGlyph(state, sty, frame)
	spans := make([]summarySpan, 0, len(summary)+len(suffix)+1)
	spans = append(spans, summary...)
	spans = append(spans, suffix...)
	if label := lifecycleSuffix(state); label != "" {
		spans = append(spans, summarySpan{text: " · " + label, kind: spanMuted})
	}
	header := glyphStyle.UnsetBackground().Render(glyph+" ") + renderSpans(spans, sty)
	return ansi.Truncate(header, max(1, width), "…")
}

func lifecycleSuffix(state toolLifecycle) string {
	switch state {
	case lifecycleAwaiting:
		return "awaiting approval"
	case lifecycleDenied:
		return "denied"
	case lifecycleCancelled:
		return "stopped"
	default:
		return ""
	}
}

func statusGlyph(state toolLifecycle, sty Styles, frame int) (string, lipgloss.Style) {
	switch state {
	case lifecycleRunning:
		glyph := sty.StatusSpinner.Frame(frame)
		if glyph == "" {
			glyph = "•"
		}
		return glyph, sty.StatusRunning
	case lifecycleAwaiting:
		return "•", sty.StatusRunning
	case lifecycleSuccess:
		return "✓", sty.StatusSuccess
	case lifecycleError:
		return "✗", sty.StatusError
	default:
		return "•", sty.Meta
	}
}

// renderActivityHeader renders the shared compact header used by thinking and
// grouped inspection activity. Keeping the state-to-glyph mapping here makes
// every progressing row use the same spinner and static fallbacks.
func renderActivityHeader(state toolLifecycle, label, suffix string, width int, sty Styles, frame int) string {
	glyph, glyphStyle := statusGlyph(state, sty, frame)
	header := glyphStyle.UnsetBackground().Render(glyph+" ") + sty.ToolName.Render(label)
	if suffix != "" {
		header += sty.ToolDetail.Render(suffix)
	}
	return ansi.Truncate(header, max(1, width), "…")
}

func renderSpans(spans []summarySpan, sty Styles) string {
	var b strings.Builder
	for _, span := range spans {
		switch span.kind {
		case spanAction:
			b.WriteString(sty.ToolName.Render(span.text))
		case spanArgument:
			b.WriteString(sty.ToolArgument.Render(span.text))
		default:
			b.WriteString(sty.ToolDetail.Render(span.text))
		}
	}
	return b.String()
}

func inspectionLifecycle(blocks []agent.Block) toolLifecycle {
	states := make([]toolLifecycle, 0, len(blocks))
	for i := range blocks {
		state := lifecycleOf(blocks[i].Tool)
		states = append(states, state)
		if state == lifecycleRunning {
			return lifecycleRunning
		}
	}
	for _, state := range states {
		if state == lifecycleAwaiting {
			return lifecycleAwaiting
		}
	}
	for _, state := range states {
		if state == lifecycleSuccess {
			return lifecycleSuccess
		}
	}
	// Keep mixed terminal outcomes quiet: surface a terminal group state only
	// when every inspection call reached the same outcome.
	all := func(want toolLifecycle) bool {
		if len(states) == 0 {
			return false
		}
		for _, state := range states {
			if state != want {
				return false
			}
		}
		return true
	}
	for _, state := range []toolLifecycle{lifecycleError, lifecycleDenied, lifecycleCancelled} {
		if all(state) {
			return state
		}
	}
	return lifecycleUnknown
}

func renderInspectionGroup(blocks []agent.Block, presentations []toolPresentation, width int, sty Styles, frame int) string {
	state := inspectionLifecycle(blocks)
	label := "inspect"
	suffix := ""
	switch state {
	case lifecycleRunning:
		label = "inspecting"
		suffix = activityEllipsis(frame, sty.StatusSpinner.Len() > 0)
	case lifecycleAwaiting:
		label, suffix = "inspecting", " · awaiting approval"
	case lifecycleSuccess:
		label = "inspected"
	case lifecycleError:
		label = "inspection failed"
	case lifecycleDenied:
		label = "inspection denied"
	case lifecycleCancelled:
		label = "inspection stopped"
	case lifecycleUnknown:
		// Keep the neutral defaults.
	}
	lines := []string{renderActivityHeader(state, label, suffix, width, sty, frame)}
	diagnosticIndex, diagnostic := inspectionDiagnostic(blocks, state)

	first := true
	for i := 0; i < len(blocks); {
		p := presentations[i]
		memberState := lifecycleOf(blocks[i].Tool)
		if p.identity == spec.ClientReadFile && (memberState == lifecycleRunning || memberState == lifecycleSuccess) {
			paths := make([]string, 0, 2)
			seen := make(map[string]struct{})
			j := i
			for j < len(blocks) && presentations[j].identity == spec.ClientReadFile {
				s := lifecycleOf(blocks[j].Tool)
				if s != lifecycleRunning && s != lifecycleSuccess {
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
			lines = append(lines, renderInspectionChild(spans, first, width, sty)...)
			first, i = false, j
			continue
		}
		lines = append(lines, renderInspectionChild(p.summary(blocks[i].Tool), first, width, sty)...)
		if i == diagnosticIndex {
			lines = append(lines, renderInspectionDiagnostic(diagnostic, first, width, sty))
		}
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

// inspectionDiagnostic returns the first available diagnostic only for a
// wholly failed inspection group. Mixed probe failures remain intentionally
// quiet.
func inspectionDiagnostic(blocks []agent.Block, state toolLifecycle) (int, string) {
	if state != lifecycleError {
		return -1, ""
	}
	for i, block := range blocks {
		if lifecycleOf(block.Tool) != lifecycleError {
			continue
		}
		detail := block.Tool.Detail
		if detail == "" {
			detail = block.Tool.Output
		}
		if detail != "" {
			return i, collapseWS(escape.Multiline(detail))
		}
	}
	return -1, ""
}

func renderInspectionDiagnostic(diagnostic string, first bool, width int, sty Styles) string {
	prefix := "      "
	if first {
		prefix = "    "
	}
	line := sty.ToolDetail.Render(prefix) + sty.ToolError.Render(ansi.Truncate(diagnostic, max(1, width-len(prefix)), "…"))
	return ansi.Truncate(line, max(1, width), "…")
}

func renderInspectionChild(spans []summarySpan, first bool, width int, sty Styles) []string {
	prefix := "    "
	if first {
		prefix = "  └ "
	}
	if len(spans) == 0 {
		return []string{sty.ToolDetail.Render(prefix)}
	}
	action := spans[0]
	if action.kind != spanAction {
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
	bodyWidth := width - indentWidth
	if bodyWidth < 1 {
		line := sty.ToolDetail.Render(prefix) + actionRendered + sty.ToolDetail.Render(" ") + renderSpans(rest, sty)
		return []string{ansi.Truncate(line, max(1, width), "…")}
	}
	wrapped := strings.Split(ansi.Wordwrap(renderSpans(rest, sty), bodyWidth, "-"), "\n")
	lines := make([]string, 0, len(wrapped))
	for i, row := range wrapped {
		lead := strings.Repeat(" ", indentWidth)
		if i == 0 {
			lead = prefix
			row = actionRendered + sty.ToolDetail.Render(" ") + row
		}
		lines = append(lines, ansi.Truncate(sty.ToolDetail.Render(lead)+row, max(1, width), "…"))
	}
	return lines
}

// collapseWS joins whitespace runs.
func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }

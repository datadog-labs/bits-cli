package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/tools/spec"
	"github.com/DataDog/bits-cli/internal/tui/styles"
)

func toolBlockOf(status agent.ToolStatus) agent.Block {
	return agent.Block{
		Kind: assistant.KindToolResult,
		Tool: &agent.ToolBlock{
			Name:      "search_logs",
			Namespace: new("datadog"),
			Input:     ` { "query": "timeout" } `,
			Output:    "ok: 4 results",
			Status:    status,
		},
	}
}

func headerOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func TestReasoningRendersAsCompactActivity(t *testing.T) {
	block := agent.Block{
		Kind:     assistant.KindReasoning,
		Thinking: &assistant.ThinkingPayload{Content: "private reasoning body"},
	}
	sty := DefaultStyles(true)

	first := ansi.Strip(RenderBlock(block, 80, sty, 0))
	second := ansi.Strip(RenderBlock(block, 80, sty, 8))
	if !strings.Contains(first, sty.StatusSpinner.Frame(0)+" thinking.") || first == second {
		t.Fatalf("active reasoning did not render compact animated status: %q, %q", first, second)
	}
	if strings.Contains(first, block.Thinking.Content) {
		t.Fatalf("active reasoning exposed its body: %q", first)
	}

	block.Complete = true
	settled := ansi.Strip(RenderBlock(block, 80, sty, 16))
	if settled != "✓ thought" {
		t.Fatalf("completed reasoning = %q, want %q", settled, "✓ thought")
	}
}

func TestGenericToolUsesQualifiedNameCompactInputAndQuietStatus(t *testing.T) {
	for _, test := range []struct {
		status agent.ToolStatus
		want   string
	}{
		{agent.ToolRunning, "datadog.search_logs({\"query\":\"timeout\"})"},
		{agent.ToolAwaitingApproval, "datadog.search_logs({\"query\":\"timeout\"}) · awaiting approval"},
		{agent.ToolSuccess, "datadog.search_logs({\"query\":\"timeout\"})"},
		{agent.ToolError, "datadog.search_logs({\"query\":\"timeout\"})"},
	} {
		got := ansi.Strip(headerOf(RenderBlock(toolBlockOf(test.status), 100, DefaultStyles(true), 0)))
		if !strings.Contains(got, test.want) {
			t.Errorf("status %v header = %q, want %q", test.status, got, test.want)
		}
		if strings.Contains(got, " running ") || strings.Contains(got, " error ") {
			t.Errorf("header retained a status pill label: %q", got)
		}
	}
}

func TestExactToolIdentityControlsSpecialization(t *testing.T) {
	local := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
		Name: "read_file", Input: `{"path":"README.md"}`, Status: agent.ToolSuccess, IsClientSide: true,
	}}
	server := local
	server.Tool = &agent.ToolBlock{Name: "read_file", Namespace: new("remote"), Input: `{"path":"README.md"}`, Output: "body", Status: agent.ToolSuccess}

	if got := ansi.Strip(RenderBlock(local, 80, DefaultStyles(true), 0)); !strings.Contains(got, "read README.md") || strings.Contains(got, "body") {
		t.Fatalf("local read rendering = %q", got)
	}
	if got := ansi.Strip(RenderBlock(server, 80, DefaultStyles(true), 0)); !strings.Contains(got, `remote.read_file({"path":"README.md"})`) || !strings.Contains(got, "body") {
		t.Fatalf("server read rendering = %q", got)
	}
}

func TestListFilesRendersExplicitDepth(t *testing.T) {
	block := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
		Name: spec.ListFiles, Input: `{"path":"internal","depth":2}`, Status: agent.ToolSuccess, IsClientSide: true,
	}}
	plain := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0))
	if !strings.Contains(plain, "list internal · depth 2") {
		t.Fatalf("list_files summary = %q, want path and depth", plain)
	}
}

func TestInvalidInspectionInputStillHidesSuccessfulOutput(t *testing.T) {
	for _, test := range []struct {
		name     string
		toolName string
		input    string
	}{
		{name: "read", toolName: "read_file", input: `{}`},
		{name: "list", toolName: "list_files", input: `{"path":`},
		{name: "search", toolName: "grep_files", input: `{"pattern":""}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			block := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
				Name:  test.toolName,
				Input: test.input, Output: "hidden inspection output", Status: agent.ToolSuccess, IsClientSide: true,
			}}
			if got := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0)); strings.Contains(got, "hidden inspection output") {
				t.Fatalf("successful invalid inspection input exposed output: %q", got)
			}
		})
	}
}

func TestLocalInspectionActionsUseLifecycleVerbsForValidAndPartialInput(t *testing.T) {
	for _, test := range []struct {
		name    string
		tool    string
		input   string
		partial bool
		status  agent.ToolStatus
		want    string
		output  bool
	}{
		{name: "reading valid path", tool: spec.ReadFile, input: `{"path":"README.md"}`, status: agent.ToolRunning, want: "reading README.md"},
		{name: "listing valid path", tool: spec.ListFiles, input: `{"path":""}`, status: agent.ToolRunning, want: "listing ."},
		{name: "searching valid pattern", tool: spec.GrepFiles, input: `{"pattern":"TODO","path":"internal"}`, status: agent.ToolRunning, want: "searching TODO in internal"},
		{name: "searching awaiting approval", tool: spec.GrepFiles, input: `{"pattern":"TODO","path":"internal"}`, status: agent.ToolAwaitingApproval, want: "searching TODO in internal · awaiting approval"},
		{name: "reading partial input", tool: spec.ReadFile, input: `{"path":"READ`, partial: true, status: agent.ToolRunning, want: "reading"},
		{name: "listing malformed settled input", tool: spec.ListFiles, input: `{"path":`, status: agent.ToolSuccess, want: "✓ list"},
		{name: "searching malformed error input", tool: spec.GrepFiles, input: `{"pattern":`, status: agent.ToolError, want: "✗ search", output: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tool := &agent.ToolBlock{
				Name:         test.tool,
				Status:       test.status,
				Output:       "inspection diagnostic",
				IsClientSide: true,
			}
			if test.partial {
				tool.InputPartial = test.input
			} else {
				tool.Input = test.input
			}
			block := agent.Block{Kind: assistant.KindToolResult, Tool: tool}

			got := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0))
			if !strings.Contains(got, test.want) {
				t.Fatalf("rendering = %q, want %q", got, test.want)
			}
			if test.output && !strings.Contains(got, "inspection diagnostic") {
				t.Fatalf("inspection diagnostic was hidden: %q", got)
			}
			if !test.output && strings.Contains(got, "inspection diagnostic") {
				t.Fatalf("inspection output was shown: %q", got)
			}
		})
	}
}

func TestRemoteInspectionNamedToolStaysGenericWithPartialInput(t *testing.T) {
	block := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
		Name:         spec.ReadFile,
		Namespace:    new("remote"),
		InputPartial: `{"path":"README`,
		Status:       agent.ToolRunning,
	}}

	got := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0))
	if !strings.Contains(got, "remote.read_file") || strings.Contains(got, "reading") {
		t.Fatalf("remote partial inspection rendering = %q", got)
	}
}

func TestExecCommandDecodesEnvelopeAndBoundsOutput(t *testing.T) {
	exit := 1
	result := fmt.Sprintf(`{"status":"nonzero_exit","exit_code":%d,"duration_ms":19,"truncated":true,"output_incomplete":true,"stdout":"out-1\nout-2\nout-3","stderr":"err-1\nerr-2\nerr-3"}`, exit)
	block := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
		Name: "exec_command", Input: `{"cmd":"go   test\n./...","workdir":".","timeout_ms":30000}`, Output: result,
		Status: agent.ToolError, IsClientSide: true,
	}}
	plain := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0))
	for _, want := range []string{"run failed go   test in . · timeout 30s · exit 1", "  │ ./...", "stdout: out-1", "err-3", "… output truncated and incomplete"} {
		if !strings.Contains(plain, want) {
			t.Errorf("exec rendering missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "␊") {
		t.Errorf("exec summary exposed a control picture:\n%s", plain)
	}
	for _, hidden := range []string{`"duration_ms"`, `"stdout"`, `"truncated"`} {
		if strings.Contains(plain, hidden) {
			t.Errorf("exec rendering exposed JSON field %q:\n%s", hidden, plain)
		}
	}
	if got, wantMax := strings.Count(plain, "\n")+1, 2+execOutputMaxLines; got > wantMax {
		t.Fatalf("exec rendering used %d lines, want <= %d:\n%s", got, wantMax, plain)
	}
}

func TestExecCommandPreservesSafeBoundedMultilineInvocation(t *testing.T) {
	command := "python3 - <<'PY'\r\n\timport json\r\nprint('long command line that must be truncated at narrow widths')\r\nline-4\r\nline-5\r\nline-6\r\nline-7\r\nline-8\r\nline-9\r\nline-10\r\nline-11\r\nline-12\r\nPY\x1b"
	input, err := json.Marshal(spec.ExecCommandInput{Cmd: command})
	if err != nil {
		t.Fatal(err)
	}
	block := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
		Name: spec.ExecCommand, Input: string(input), Status: agent.ToolSuccess, IsClientSide: true,
	}}

	plain := ansi.Strip(RenderBlock(block, 32, DefaultStyles(true), 0))
	want := strings.Join([]string{
		"✓ ran python3 - <<'PY'",
		"  │     import json",
		"  │ print('long command line th…",
		"  │ … +4 lines",
		"  │ line-8",
		"  │ line-9",
		"  │ line-10",
		"  │ line-11",
		"  │ line-12",
		"  │ PY␛",
	}, "\n")
	if plain != want {
		t.Fatalf("multiline exec rendering:\n%s\nwant:\n%s", plain, want)
	}
	if got := strings.Count(plain, "\n") + 1; got != execCommandMaxLines {
		t.Fatalf("multiline invocation used %d lines, want %d", got, execCommandMaxLines)
	}
	for _, row := range strings.Split(RenderBlock(block, 32, DefaultStyles(true), 0), "\n") {
		if got := ansi.StringWidth(row); got > 32 {
			t.Fatalf("row width = %d, want <= 32: %q", got, ansi.Strip(row))
		}
	}
}

func TestExecCommandHighlightsQuotedHeredocAsOneShellDocument(t *testing.T) {
	input, err := json.Marshal(spec.ExecCommandInput{Cmd: "python3 - <<'PY'\nimport json\npayload = {\"enabled\": True}\nprint(payload[\"enabled\"])\nPY"})
	if err != nil {
		t.Fatal(err)
	}
	block := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
		Name: spec.ExecCommand, Input: string(input), Status: agent.ToolSuccess, IsClientSide: true,
	}}
	sty := DefaultStyles(true)
	sty.StatusSuccess = lipgloss.NewStyle()
	sty.ToolName = lipgloss.NewStyle()
	sty.ToolArgument = lipgloss.NewStyle()
	sty.ToolDetail = lipgloss.NewStyle()

	rows := strings.Split(RenderBlock(block, 80, sty, 0), "\n")
	if got, want := len(rows), 5; got != want {
		t.Fatalf("rendered %d rows, want %d", got, want)
	}
	if !strings.ContainsRune(rows[0], '\x1b') {
		t.Fatalf("exec header was not syntax highlighted: %q", rows[0])
	}
	var heredocStyle string
	for i, row := range rows[1:] {
		start := strings.Index(row, "\x1b[")
		if start < 0 {
			t.Fatalf("heredoc row %d was not highlighted: %q", i+1, row)
		}
		end := strings.Index(row[start:], "m")
		if end < 0 {
			t.Fatalf("heredoc row %d has an incomplete style: %q", i+1, row)
		}
		style := row[start : start+end+1]
		if heredocStyle == "" {
			heredocStyle = style
		} else if style != heredocStyle {
			t.Errorf("heredoc row %d style = %q, want literal style %q", i+1, style, heredocStyle)
		}
	}
}

func TestExecCommandApprovalUsesSpecializedMultilineRenderer(t *testing.T) {
	input, err := json.Marshal(spec.ExecCommandInput{Cmd: "python3 - <<'PY'\r\nprint('hello')\r\nPY"})
	if err != nil {
		t.Fatal(err)
	}
	tool := &agent.ToolBlock{
		Name:         spec.ExecCommand,
		Input:        string(input),
		Status:       agent.ToolAwaitingApproval,
		IsClientSide: true,
		Approval: &agent.ApprovalPrompt{
			Detail: "cwd: /workspace · unsandboxed",
		},
	}
	sty := DefaultStyles(true)
	sty.ToolArgument = lipgloss.NewStyle()
	sty.ToolDetail = lipgloss.NewStyle()

	rendered, ok := RenderToolApproval(tool, 80, sty)
	if !ok {
		t.Fatal("exec_command did not select its approval renderer")
	}
	plain := ansi.Strip(rendered)
	if want := "python3 - <<'PY'\nprint('hello')\nPY\ncwd: /workspace · unsandboxed"; plain != want {
		t.Fatalf("exec approval rendering:\n%s\nwant:\n%s", plain, want)
	}
	if strings.Contains(plain, "␊") {
		t.Fatalf("exec approval exposed newline as a control picture: %q", plain)
	}
	if !strings.ContainsRune(rendered, '\x1b') {
		t.Fatalf("exec approval was not syntax highlighted: %q", rendered)
	}

	if rendered, ok := RenderToolApproval(&agent.ToolBlock{Name: "other", Approval: &agent.ApprovalPrompt{Detail: "generic"}}, 80, sty); ok || rendered != "" {
		t.Fatalf("generic tool selected specialized approval rendering: %q, %v", rendered, ok)
	}
}

func TestToolRowsStayWithinWidth(t *testing.T) {
	blocks := []agent.Block{
		toolBlockOf(agent.ToolSuccess),
		{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{Name: spec.ExecCommand, Input: `{"cmd":"a very long command with many arguments","workdir":"a/long/workdir"}`, Output: `{"status":"success","stdout":"a very long output line that must wrap safely","stderr":""}`, Status: agent.ToolSuccess, IsClientSide: true}},
		{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{Name: spec.ExecCommand, Input: `{"cmd":"first line\nsecond very long command line with many arguments\nlast line"}`, Status: agent.ToolSuccess, IsClientSide: true}},
	}
	for _, width := range []int{12, 24} {
		for _, block := range blocks {
			for _, row := range strings.Split(RenderBlock(block, width, DefaultStyles(true), 0), "\n") {
				if got := ansi.StringWidth(row); got > width {
					t.Errorf("row width = %d, want <= %d: %q", got, width, ansi.Strip(row))
				}
			}
		}
	}
}

func TestOnlyRunningToolAnimationDependsOnFrame(t *testing.T) {
	sty := DefaultStyles(true)
	running := toolBlockOf(agent.ToolRunning)
	if RenderBlock(running, 80, sty, 0) == RenderBlock(running, 80, sty, 4) {
		t.Fatal("running tool did not animate")
	}
	for _, status := range []agent.ToolStatus{agent.ToolAwaitingApproval, agent.ToolSuccess, agent.ToolError} {
		block := toolBlockOf(status)
		if RenderBlock(block, 80, sty, 0) != RenderBlock(block, 80, sty, 17) {
			t.Errorf("status %v changed with frame", status)
		}
	}
	flat := StylesFor(styles.Default(true).WithoutMotion())
	if RenderBlock(running, 80, flat, 0) != RenderBlock(running, 80, flat, 17) {
		t.Fatal("motion-disabled running tool changed with frame")
	}
}

func TestDeniedAndCancelledTakePrecedenceOverRawError(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*agent.ToolBlock)
		want string
	}{
		{"denied", func(tool *agent.ToolBlock) { tool.Denied = true }, "• datadog.search_logs({\"query\":\"timeout\"}) · denied"},
		{"cancelled", func(tool *agent.ToolBlock) { tool.Cancelled = true }, "• datadog.search_logs({\"query\":\"timeout\"}) · stopped"},
	} {
		t.Run(test.name, func(t *testing.T) {
			block := toolBlockOf(agent.ToolError)
			test.set(block.Tool)
			got := ansi.Strip(RenderBlock(block, 100, DefaultStyles(true), 0))
			if !strings.Contains(got, test.want) || strings.Contains(got, "ok: 4 results") {
				t.Fatalf("rendering = %q, want %q without result body", got, test.want)
			}
		})
	}
}

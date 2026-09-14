package chat

import (
	"fmt"
	"strings"
	"testing"

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

func TestExecCommandDecodesEnvelopeAndBoundsOutput(t *testing.T) {
	exit := 1
	result := fmt.Sprintf(`{"status":"nonzero_exit","exit_code":%d,"duration_ms":19,"truncated":true,"output_incomplete":true,"stdout":"out-1\nout-2\nout-3","stderr":"err-1\nerr-2\nerr-3"}`, exit)
	block := agent.Block{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{
		Name: "exec_command", Input: `{"cmd":"go   test\n./...","workdir":"."}`, Output: result,
		Status: agent.ToolError, IsClientSide: true,
	}}
	plain := ansi.Strip(RenderBlock(block, 80, DefaultStyles(true), 0))
	for _, want := range []string{"execution failed go   test␊./... in . · exit 1", "stdout: out-1", "err-3", "… output truncated and incomplete"} {
		if !strings.Contains(plain, want) {
			t.Errorf("exec rendering missing %q:\n%s", want, plain)
		}
	}
	for _, hidden := range []string{`"duration_ms"`, `"stdout"`, `"truncated"`} {
		if strings.Contains(plain, hidden) {
			t.Errorf("exec rendering exposed JSON field %q:\n%s", hidden, plain)
		}
	}
	if got := strings.Count(plain, "\n") + 1; got > 1+execOutputMaxLines {
		t.Fatalf("exec rendering used %d lines, want <= %d:\n%s", got, 1+execOutputMaxLines, plain)
	}
}

func TestToolRowsStayWithinWidth(t *testing.T) {
	blocks := []agent.Block{
		toolBlockOf(agent.ToolSuccess),
		{Kind: assistant.KindToolResult, Tool: &agent.ToolBlock{Name: spec.ExecCommand, Input: `{"cmd":"a very long command with many arguments","workdir":"a/long/workdir"}`, Output: `{"status":"success","stdout":"a very long output line that must wrap safely","stderr":""}`, Status: agent.ToolSuccess, IsClientSide: true}},
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

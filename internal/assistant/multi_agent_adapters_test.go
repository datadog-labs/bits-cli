package assistant

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMultiAgentApprovalInputUsesLocalCorrelation(t *testing.T) {
	input := `{"tool_name":"write_dashboard","tool_args":{"title":"CPU"},"tool_call_id":"wire-1","approval_message":"Publish dashboard?","tool_activation_source":"builtin"}`
	for _, localID := range []string{"multi-agent:root:1:wire-1", "multi-agent:child:2:wire-1"} {
		got, err := adaptMultiAgentToolInput(ApprovalRequestTool, input, "wire-1", localID)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got), &fields); err != nil {
			t.Fatal(err)
		}
		if string(fields["tool_call_id"]) != `"`+localID+`"` || string(fields["tool_name"]) != `"write_dashboard"` || string(fields["tool_args"]) != `{"title":"CPU"}` || string(fields["approval_message"]) != `"Publish dashboard?"` || string(fields["tool_activation_source"]) != `"builtin"` {
			t.Fatalf("approval input lost correlation or fields: %s", got)
		}
	}
	ordinary := `{"tool_call_id":"user-data"}`
	if got, err := adaptMultiAgentToolInput("read_file", ordinary, "wire", "local"); err != nil || got != ordinary {
		t.Fatalf("ordinary tool input = %q, %v", got, err)
	}
	for _, invalid := range []string{`{`, `null`, `[]`, `{}`} {
		if _, err := adaptMultiAgentToolInput(ApprovalRequestTool, invalid, "wire", "local"); err == nil {
			t.Fatalf("invalid approval input accepted: %s", invalid)
		}
	}
	if _, err := adaptMultiAgentToolInput(ApprovalRequestTool, input, "different-wire-id", "local"); err == nil {
		t.Fatal("approval with mismatched inner ID was accepted")
	}
}

func TestMultiAgentDashboardKeepsStructuredFields(t *testing.T) {
	raw := json.RawMessage(`{"title":"CPU dashboard","description":"CPU across hosts","layout_type":"free","reflow_type":"auto","widgets":[{"definition":{"type":"timeseries"}}],"template_variables":[{"name":"host"}]}`)
	got, err := multiAgentDashboardPayload(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := &DashboardPayload{
		Title: "CPU dashboard", Description: "CPU across hosts", LayoutType: "free", ReflowType: "auto",
		Widgets: json.RawMessage(`[{"definition":{"type":"timeseries"}}]`), TemplateVariables: json.RawMessage(`[{"name":"host"}]`),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dashboard = %#v, want %#v", got, want)
	}
	defaults, err := multiAgentDashboardPayload(json.RawMessage(`{"title":"Empty dashboard"}`))
	if err != nil || defaults.Title != "Empty dashboard" || defaults.LayoutType != "ordered" || defaults.ReflowType != "fixed" || string(defaults.Widgets) != `[]` || string(defaults.TemplateVariables) != `[]` {
		t.Fatalf("dashboard defaults = %#v, %v", defaults, err)
	}
}

func TestMultiAgentToolResultsPreserveSmallPayload(t *testing.T) {
	results := []multiAgentToolResult{{RunID: "run", AgentID: "child", Generation: 2, ToolCallID: "wire", Status: "success", Output: `{"ok":true}`, Title: "Read", Content: "Display"}}
	want, err := json.Marshal(multiAgentToolResultsRequest{Results: results})
	if err != nil {
		t.Fatal(err)
	}
	got, err := encodeMultiAgentToolResults(results)
	if err != nil || string(got) != string(want) {
		t.Fatalf("small result = %s, %v; want %s", got, err, want)
	}
}

func TestMultiAgentToolResultsBoundTransportWithoutChangingLocalResults(t *testing.T) {
	for _, text := range []string{strings.Repeat("é", 1_100_000), strings.Repeat("\x00<", 600_000)} {
		results := []multiAgentToolResult{
			{RunID: "run", AgentID: "root", Generation: 1, ToolCallID: "call-a", Status: "success", Output: text, Title: strings.Repeat("é", 600), Content: text},
			{RunID: "run", AgentID: "child", Generation: 2, ToolCallID: "call-b", Status: "error", Output: text, Title: "Failure", Content: text},
		}
		before := append([]multiAgentToolResult(nil), results...)
		body, err := encodeMultiAgentToolResults(results)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > 4_000_000 {
			t.Fatalf("encoded body has %d bytes", len(body))
		}
		var decoded multiAgentToolResultsRequest
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		total := 0
		for i, result := range decoded.Results {
			if result.RunID != before[i].RunID || result.AgentID != before[i].AgentID || result.Generation != before[i].Generation || result.ToolCallID != before[i].ToolCallID || result.Status != before[i].Status {
				t.Fatalf("result correlation changed: %#v", result)
			}
			if utf8.RuneCountInString(result.Output) > 1_000_000 || utf8.RuneCountInString(result.Content) > 1_000_000 || utf8.RuneCountInString(result.Title) > 512 {
				t.Fatal("field exceeds server character limit")
			}
			for _, value := range []string{result.Output, result.Title, result.Content} {
				if !utf8.ValidString(value) {
					t.Fatal("transport text is invalid UTF-8")
				}
				total += len(value)
			}
			if !strings.Contains(result.Output, "Truncated") || !strings.Contains(result.Content, "Truncated") {
				t.Fatal("truncation was not marked")
			}
			if i == 1 && result.Title != "Failure" {
				t.Fatalf("short title changed to %q", result.Title)
			}
		}
		if total > 3_500_000 {
			t.Fatalf("combined text has %d UTF-8 bytes", total)
		}
		if !reflect.DeepEqual(results, before) {
			t.Fatal("transport encoding changed local results")
		}
	}
}

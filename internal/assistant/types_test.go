package assistant

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestContentDecodesApps(t *testing.T) {
	handle := `"` + strings.Repeat("h", 43) + `"`
	saved := `{"type":"mcp_app_saved","version":1,"tool_call_id":"c1","tool_name":"show_ui","input_digest":"d","server_identity":"s","resource_uri":"ui://apps/dataviz","resource_handle":"dataviz-mcp-ui@prototype"}`
	for _, tt := range []struct {
		name, input string
		want        AppPayload
	}{
		{
			name:  "live with relay and restoration",
			input: `{"type":"mcp_app","tool_call_id":"c1","tool_name":"show_ui","tool_input":{"data_tool_name":"get_widget"},"resource_uri":"ui://apps/dataviz","resource_handle":"dataviz-mcp-ui@prototype","relay_handle":` + handle + `,"tool_result":{"content":[]},"restoration":` + saved + `}`,
			want: AppPayload{
				ToolCallID: "c1", ToolName: "show_ui", ToolInput: json.RawMessage(`{"data_tool_name":"get_widget"}`),
				ResourceURI: "ui://apps/dataviz", Result: json.RawMessage(`{"content":[]}`),
				RelayHandle: strings.Repeat("h", 43), Restorable: true,
			},
		},
		{
			name:  "live without relay or restoration",
			input: `{"type":"mcp_app","tool_call_id":"c1","tool_name":"get_widget","tool_input":{},"resource_uri":"ui://apps/dataviz","relay_handle":null,"tool_result":{},"restoration":null}`,
			want:  AppPayload{ToolCallID: "c1", ToolName: "get_widget", ToolInput: json.RawMessage(`{}`), ResourceURI: "ui://apps/dataviz", Result: json.RawMessage(`{}`)},
		},
		{
			name:  "saved",
			input: saved,
			want:  AppPayload{ToolCallID: "c1", ToolName: "show_ui", ResourceURI: "ui://apps/dataviz", Restorable: true},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var c Content
			if err := json.Unmarshal([]byte(tt.input), &c); err != nil {
				t.Fatal(err)
			}
			if c.Kind() != KindApp || c.App == nil {
				t.Fatalf("content = %+v", c)
			}
			if !reflect.DeepEqual(*c.App, tt.want) {
				t.Fatalf("app = %+v, want %+v", *c.App, tt.want)
			}
		})
	}
}

func TestViewSummary(t *testing.T) {
	chart := func(window string) string {
		return `{"structuredContent":{"widget_definition":{"type":"timeseries","title":"CPU"}` + window + `}}`
	}
	for _, tt := range []struct {
		name, result string
		title        string
		chart        bool
		start, end   int64 // Unix seconds; zero for no window
	}{
		{name: "chart", result: chart(`,"from_ts":10,"to_ts":20`), title: "CPU", chart: true, start: 10, end: 20},
		{name: "chart without window", result: chart(``), title: "CPU", chart: true},
		{name: "chart with inverted window", result: chart(`,"from_ts":20,"to_ts":10`), title: "CPU", chart: true},
		{name: "show_ui view", result: `{"_meta":{"datadog/show_ui":{"view_spec":{"view":{"name":"incident_detail","version":1}}}}}`, title: "Incident detail"},
		{name: "chart from any source tool", result: `{"structuredContent":{"source_tool":"renamed","widget_definition":{"type":"timeseries"}}}`, chart: true},
		{name: "other payload", result: `{"structuredContent":{"incident_id":"1"}}`},
		{name: "invalid widget", result: `{"structuredContent":{"widget_definition":{}}}`},
		{name: "saved"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app := AppPayload{Result: json.RawMessage(tt.result)}
			if tt.result == "" {
				app.Result = nil
			}
			got := app.Summary()
			if got.Title != tt.title || (got.Chart != nil) != tt.chart {
				t.Fatalf("summary = %+v", got)
			}
			if got.Chart == nil {
				return
			}
			var start, end int64
			if !got.Chart.End.IsZero() {
				start, end = got.Chart.Start.Unix(), got.Chart.End.Unix()
			}
			if start != tt.start || end != tt.end || !strings.Contains(string(got.Chart.Definition), `"type":"timeseries"`) {
				t.Fatalf("chart = %+v", got.Chart)
			}
		})
	}
}

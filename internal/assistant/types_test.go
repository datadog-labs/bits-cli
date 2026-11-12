package assistant

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWidgetDefinitionRoundTrip(t *testing.T) {
	input := []byte(`{"type":"timeseries","title":"CPU","requests":[{"q":"avg:cpu{*}"}],"time":{"type":"fixed","from":1700000000000,"to":1700003600000,"hide_incomplete_cost_data":true}}`)
	var def WidgetDefinition
	if err := json.Unmarshal(input, &def); err != nil {
		t.Fatal(err)
	}
	if !def.Valid() || def.Type != "timeseries" || def.Title != "CPU" || def.Time == nil || def.Time.From != 1700000000000 {
		t.Fatalf("decoded widget = %+v", def)
	}
	if from, to, ok := def.TimeRange(time.Now()); !ok || from != 1700000000000 || to != 1700003600000 {
		t.Fatalf("time range = %d..%d, %v", from, to, ok)
	}
	encoded, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(input, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("widget changed in transit: %s", encoded)
	}
}

func TestWidgetDefinitionTimeForms(t *testing.T) {
	for _, input := range []string{
		`{"type":"timeseries","time":{"type":"live","value":15,"unit":"minute"}}`,
		`{"type":"timeseries","time":{"type":"weekly","offset":0}}`,
		`{"type":"timeseries","time":{"live_span":"1mo"}}`,
		`{"type":"timeseries","time":{"hide_incomplete_cost_data":true}}`,
	} {
		var def WidgetDefinition
		if err := json.Unmarshal([]byte(input), &def); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		var before, after map[string]any
		if err := json.Unmarshal([]byte(input), &before); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &after); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Errorf("widget changed in transit: %s", encoded)
		}
	}
}

func TestWidgetDefinitionTypedFieldsOverrideDecodedJSON(t *testing.T) {
	var def WidgetDefinition
	input := []byte(`{"type":"timeseries","title":"old","requests":[{"q":"avg:cpu{*}"}],"time":{"type":"fixed","from":1000,"to":2000,"hide_incomplete_cost_data":true}}`)
	if err := json.Unmarshal(input, &def); err != nil {
		t.Fatal(err)
	}
	def.Title = ""
	def.Time.Type = "live"
	def.Time.Value = 15
	def.Time.Unit = "minute"
	encoded, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["title"]; ok {
		t.Fatalf("cleared title remained in %s", encoded)
	}
	var timing map[string]json.RawMessage
	if err := json.Unmarshal(got["time"], &timing); err != nil {
		t.Fatal(err)
	}
	if _, ok := timing["from"]; ok {
		t.Fatalf("old fixed range remained in %s", encoded)
	}
	if _, ok := timing["to"]; ok {
		t.Fatalf("old fixed range remained in %s", encoded)
	}
	if string(timing["type"]) != `"live"` || string(timing["value"]) != "15" || string(timing["hide_incomplete_cost_data"]) != "true" || len(got["requests"]) == 0 {
		t.Fatalf("typed or unknown fields changed in %s", encoded)
	}
	def.Time = nil
	encoded, err = json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["time"]; ok {
		t.Fatalf("cleared time remained in %s", encoded)
	}
}

// Live spans end now; fixed windows win; unknown or bad settings fall back.
func TestWidgetDefinitionTimeRange(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	monthSpan := now.Sub(now.AddDate(0, -1, 0))
	live := func(value float64, unit string) *WidgetTime {
		return &WidgetTime{Type: "live", Value: value, Unit: unit}
	}
	for name, tc := range map[string]struct {
		setting *WidgetTime
		span    time.Duration
		ok      bool
	}{
		"year":                    {setting: live(1.0, "year"), span: 365 * 24 * time.Hour, ok: true},
		"weeks":                   {setting: live(2.0, "week"), span: 14 * 24 * time.Hour, ok: true},
		"minutes":                 {setting: live(15.0, "minute"), span: 15 * time.Minute, ok: true},
		"many minutes":            {setting: live(1440, "minute"), span: 24 * time.Hour, ok: true},
		"month":                   {setting: live(1, "month"), span: monthSpan, ok: true},
		"legacy":                  {setting: &WidgetTime{LiveSpan: "1mo"}, span: monthSpan, ok: true},
		"legacy bad":              {setting: &WidgetTime{LiveSpan: "week_to_date"}},
		"calendar":                {setting: &WidgetTime{Type: "weekly", Offset: 0}},
		"calendar with endpoints": {setting: &WidgetTime{Type: "weekly", From: 1000, To: 2000}},
		"unknown unit":            {setting: live(1.0, "fortnight")},
		"fractional fixed":        {setting: &WidgetTime{Type: "fixed", From: 1000.5, To: 2000}},
		"overflow":                {setting: live(1000.0, "year")},
		"legacy overflow":         {setting: &WidgetTime{LiveSpan: "1000y"}},
		"zero":                    {setting: live(0.0, "day")},
		"fractional":              {setting: live(1.5, "day")},
		"none":                    {},
	} {
		t.Run(name, func(t *testing.T) {
			from, to, ok := (WidgetDefinition{Time: tc.setting}).TimeRange(now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && (to != now.UnixMilli() || to-from != tc.span.Milliseconds()) {
				t.Fatalf("range = %d..%d, want a %s window ending now", from, to, tc.span)
			}
		})
	}
	fixed := &WidgetTime{Type: "fixed", From: 1000, To: 2000}
	if from, to, ok := (WidgetDefinition{Time: fixed}).TimeRange(now); !ok || from != 1000 || to != 2000 {
		t.Fatalf("fixed = %d..%d, %v", from, to, ok)
	}
	seconds := WidgetDefinition{Time: &WidgetTime{Type: "fixed", From: 1001, To: 2001}}
	if from, to, ok := seconds.TimeRangeSeconds(now); !ok || from != 1 || to != 3 {
		t.Fatalf("MCP seconds = %d..%d, %v", from, to, ok)
	}
	monthEnd := time.Date(2025, time.March, 31, 12, 34, 0, 0, time.UTC)
	wantStart := time.Date(2025, time.February, 28, 12, 34, 0, 0, time.UTC).UnixMilli()
	for _, setting := range []*WidgetTime{live(1, "month"), {LiveSpan: "1mo"}} {
		from, to, ok := (WidgetDefinition{Time: setting}).TimeRange(monthEnd)
		if !ok || from != wantStart || to != monthEnd.UnixMilli() {
			t.Fatalf("month-end range = %d..%d, %v", from, to, ok)
		}
	}
}

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

func TestAppSummary(t *testing.T) {
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
			if start != tt.start || end != tt.end || got.Chart.Definition.Type != "timeseries" {
				t.Fatalf("chart = %+v", got.Chart)
			}
		})
	}
}

package assistant

import (
	"encoding/json"
	"testing"
	"time"
)

func TestLegacyWidgetHistory(t *testing.T) {
	now := time.Date(2024, time.March, 31, 10, 2, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, timing string
		start, end   time.Time
	}{
		{
			name: "fixed milliseconds from history", timing: `{"type":"fixed","from":1700000000123.0,"to":1700000030456.0}`,
			start: time.UnixMilli(1700000000123), end: time.UnixMilli(1700000030456),
		},
		{
			name: "live duration", timing: `{"type":"live","unit":"hour","value":4}`,
			start: now.Add(-4 * time.Hour), end: now,
		},
		{
			name: "legacy rolling month clamps to leap day", timing: `{"live_span":"1mo"}`,
			start: time.Date(2024, time.February, 29, 10, 2, 0, 0, time.UTC), end: now,
		},
		{name: "calendar range uses renderer default", timing: `{"type":"weekly","offset":1}`},
		{name: "invalid fixed range uses renderer default", timing: `{"type":"fixed","from":20,"to":10}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := `{"type":"timeseries","title":"CPU","requests":[{"q":"avg:system.cpu.user{*}"}],"time":` + tc.timing + `}`
			var content Content
			if err := json.Unmarshal([]byte(`{"type":"widget_def","title":"fallback","widget_def":`+def+`}`), &content); err != nil {
				t.Fatal(err)
			}
			if content.Kind() != KindWidget || content.Widget == nil || content.Widget.Title != "CPU" {
				t.Fatalf("content = %+v", content)
			}
			chart := content.Widget.Chart(now)
			if chart == nil || !chart.Start.Equal(tc.start) || !chart.End.Equal(tc.end) || string(chart.Definition) != def {
				t.Fatalf("chart = %+v, want range %v to %v and original definition", chart, tc.start, tc.end)
			}
		})
	}
	var tile Content
	if err := json.Unmarshal([]byte(`{"type":"widget","title":"Prior tile","tile_def":{"q":"metrics"}}`), &tile); err != nil {
		t.Fatal(err)
	}
	if tile.Kind() != KindWidget || tile.Widget == nil || tile.Widget.Title != "Prior tile" || tile.Widget.Chart(now) != nil {
		t.Fatalf("tile = %+v", tile)
	}
}

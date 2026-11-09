package viz

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// The web sandbox decodes what its own builder encodes: gzip, unpadded
// base64url, and the widget's millisecond window, or none for its default.
func TestSandboxURL(t *testing.T) {
	for name, tc := range map[string]struct {
		time       any
		start, end string
	}{
		"backend window": {time: map[string]any{"type": "fixed", "from": 1700000000000.0, "to": 1700003600000.0}, start: "1700000000000", end: "1700003600000"},
		"no window":      {},
		"inverted":       {time: map[string]any{"type": "fixed", "from": 2000.0, "to": 1000.0}},
		"absurd":         {time: map[string]any{"type": "fixed", "from": 1e20, "to": 1e21}},
	} {
		t.Run(name, func(t *testing.T) {
			def := assistant.WidgetDefinition{"type": "timeseries", "requests": []any{map[string]any{"q": "avg:cpu{*}"}}}
			if tc.time != nil {
				def["time"] = tc.time
			}
			raw, err := SandboxURL("https://app.datadoghq.com", def)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if u.Host != "app.datadoghq.com" || u.Path != "/dataviz/sandbox" {
				t.Fatalf("url = %s", raw)
			}
			q := u.Query()
			if q.Get("start") != tc.start || q.Get("end") != tc.end {
				t.Fatalf("range = %q..%q, want %q..%q", q.Get("start"), q.Get("end"), tc.start, tc.end)
			}
			encoded := q.Get("widgetDef")
			if strings.ContainsAny(encoded, "=+/") {
				t.Fatalf("widgetDef is not unpadded base64url: %q", encoded)
			}
			zipped, err := base64.RawURLEncoding.DecodeString(encoded)
			if err != nil {
				t.Fatal(err)
			}
			r, err := gzip.NewReader(strings.NewReader(string(zipped)))
			if err != nil {
				t.Fatal(err)
			}
			var got assistant.WidgetDefinition
			if err := json.NewDecoder(r).Decode(&got); err != nil || got["type"] != "timeseries" {
				t.Fatalf("decoded = %v, %v", got, err)
			}
		})
	}
	live, err := SandboxURL("https://app.datadoghq.com", assistant.WidgetDefinition{
		"type": "timeseries", "time": map[string]any{"type": "live", "value": 1.0, "unit": "week"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := url.Parse(live); u.Query().Get("start") == "" || u.Query().Get("end") == "" {
		t.Fatalf("live span dropped its range: %s", live)
	}
	if _, err := SandboxURL("app.datadoghq.com", assistant.WidgetDefinition{}); err == nil {
		t.Fatal("an origin without a scheme built a relative link")
	}
}

// Live spans end now; fixed windows win; unknown or bad settings fall back.
func TestWidgetRange(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000)
	live := func(value any, unit string) map[string]any {
		return map[string]any{"type": "live", "value": value, "unit": unit}
	}
	for name, tc := range map[string]struct {
		setting any
		span    time.Duration
		ok      bool
	}{
		"year":            {setting: live(1.0, "year"), span: 365 * 24 * time.Hour, ok: true},
		"weeks":           {setting: live(2.0, "week"), span: 14 * 24 * time.Hour, ok: true},
		"minutes":         {setting: live(15.0, "minute"), span: 15 * time.Minute, ok: true},
		"legacy":          {setting: map[string]any{"live_span": "1mo"}, span: 30 * 24 * time.Hour, ok: true},
		"legacy bad":      {setting: map[string]any{"live_span": "week_to_date"}},
		"calendar":        {setting: map[string]any{"type": "weekly", "offset": 0.0}},
		"unknown unit":    {setting: live(1.0, "fortnight")},
		"overflow":        {setting: live(1000.0, "year")},
		"legacy overflow": {setting: map[string]any{"live_span": "1000y"}},
		"zero":            {setting: live(0.0, "day")},
		"fractional":      {setting: live(1.5, "day")},
		"string value":    {setting: live("1", "day")},
		"none":            {},
	} {
		t.Run(name, func(t *testing.T) {
			from, to, ok := widgetRange(tc.setting, now)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && (to != now.UnixMilli() || to-from != tc.span.Milliseconds()) {
				t.Fatalf("range = %d..%d, want a %s window ending now", from, to, tc.span)
			}
		})
	}
	fixed := map[string]any{"type": "fixed", "from": 1000.0, "to": 2000.0}
	if from, to, ok := widgetRange(fixed, now); !ok || from != 1000 || to != 2000 {
		t.Fatalf("fixed = %d..%d, %v", from, to, ok)
	}
}

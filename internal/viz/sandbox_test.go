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
// base64url, and the chart's millisecond window, or none for its default.
func TestSandboxURL(t *testing.T) {
	for name, tc := range map[string]struct {
		window     [2]int64 // Unix seconds
		start, end string
	}{
		"window":    {window: [2]int64{1700000000, 1700003600}, start: "1700000000000", end: "1700003600000"},
		"no window": {},
	} {
		t.Run(name, func(t *testing.T) {
			chart := assistant.Chart{Definition: assistant.WidgetDefinition{Type: "timeseries", Fields: map[string]json.RawMessage{"requests": json.RawMessage(`[{"q":"avg:cpu{*}"}]`)}}}
			if tc.window[1] > 0 {
				chart.Start, chart.End = time.Unix(tc.window[0], 0), time.Unix(tc.window[1], 0)
			}
			raw, err := SandboxURL("https://app.datadoghq.com", chart)
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
			if err := json.NewDecoder(r).Decode(&got); err != nil || got.Type != "timeseries" {
				t.Fatalf("decoded = %v, %v", got, err)
			}
		})
	}
	if _, err := SandboxURL("app.datadoghq.com", assistant.Chart{}); err == nil {
		t.Fatal("an origin without a scheme built a relative link")
	}
}

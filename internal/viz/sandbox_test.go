package viz

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

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
	if _, err := SandboxURL("app.datadoghq.com", assistant.WidgetDefinition{}); err == nil {
		t.Fatal("an origin without a scheme built a relative link")
	}
}

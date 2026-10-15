package assistant

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestParseCustomHeaders(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want http.Header
	}{
		{name: "unset", raw: "", want: nil},
		{name: "whitespace", raw: "   ", want: nil},
		{name: "trailing comma", raw: "a=1,", want: http.Header{"A": {"1"}}},
		{
			name: "single pair",
			raw:  "x-route-hint=1",
			want: http.Header{"X-Route-Hint": {"1"}},
		},
		{
			name: "several pairs with padding",
			raw:  " x-route-hint = 1 , x-trace-me=abc ",
			want: http.Header{"X-Route-Hint": {"1"}, "X-Trace-Me": {"abc"}},
		},
		{
			name: "value keeps later equals signs",
			raw:  "x-token=a=b=c",
			want: http.Header{"X-Token": {"a=b=c"}},
		},
		{name: "empty value", raw: "x-empty=", want: http.Header{"X-Empty": {""}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCustomHeaders(tc.raw)
			if err != nil {
				t.Fatalf("ParseCustomHeaders(%q) = %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseCustomHeaders(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParseCustomHeadersRejects(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"no separator", "just-a-name"},
		{"empty name", "=1"},
		{"name with space", "bad name=1"},
		{"name with colon", "bad:name=1"},
		{"name with newline", "bad\nname=1"},
		{"value with CRLF", "x-a=1\r\nauthorization: Bearer x"},
		{"value with newline", "x-a=1\nx"},
		{"value non-ascii", "x-a=café"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseCustomHeaders(tc.raw); err == nil {
				t.Errorf("ParseCustomHeaders(%q) = nil error, want a rejection", tc.raw)
			}
		})
	}
}

func TestParseCustomHeadersRejectsManagedHeaders(t *testing.T) {
	// Credentials and the client's own headers must not be settable, in any
	// casing, so a request can never present an identity other than the one the
	// selected authentication mode established.
	for _, raw := range []string{
		"authorization=Bearer attacker",
		"Authorization=Bearer attacker",
		"AUTHORIZATION=Bearer attacker",
		"dd-api-key=leak",
		"DD-APPLICATION-KEY=leak",
		"content-type=text/plain",
		"x-datadog-bits-surface=browser",
		// Go sends the host from the request URL, so accepting this would be a
		// silent no-op.
		"host=backend.example",
		"Host=backend.example",
	} {
		if _, err := ParseCustomHeaders(raw); err == nil {
			t.Errorf("ParseCustomHeaders(%q) = nil error, want a rejection", raw)
		}
	}
}

func TestRequestCarriesCustomHeaders(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-route-hint"); got != "1" {
			t.Errorf("x-route-hint = %q, want 1", got)
		}
		if r.Header.Get("DD-API-KEY") != "api" || r.Header.Get("DD-APPLICATION-KEY") != "app" {
			t.Error("credentials were displaced by a custom header")
		}
		if got := r.Header.Get("X-Datadog-Bits-Surface"); got != "cli" {
			t.Errorf("surface = %q, want cli", got)
		}
		_, _ = fmt.Fprint(w, `{"data":{"id":"u","type":"user","attributes":{}}}`)
	})
	c.CustomHeaders = http.Header{"X-Route-Hint": {"1"}}
	_, _ = c.CurrentUser(context.Background())
}

// A value that bypassed ParseCustomHeaders still must not displace a managed
// header, since the client sets those last.
func TestManagedHeadersWinOverUnvalidatedCustomHeaders(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want it absent for api-key auth", got)
		}
		if r.Header.Get("DD-API-KEY") != "api" {
			t.Error("DD-API-KEY was displaced")
		}
		_, _ = fmt.Fprint(w, `{"data":{"id":"u","type":"user","attributes":{}}}`)
	})
	c.CustomHeaders = http.Header{
		"Authorization": {"Bearer attacker"},
		"Dd-Api-Key":    {"attacker"},
	}
	_, _ = c.CurrentUser(context.Background())
}

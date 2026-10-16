package config

import (
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
			got, err := parseCustomHeaders(tc.raw)
			if err != nil {
				t.Fatalf("parseCustomHeaders(%q) = %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseCustomHeaders(%q) = %v, want %v", tc.raw, got, tc.want)
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
			if _, err := parseCustomHeaders(tc.raw); err == nil {
				t.Errorf("parseCustomHeaders(%q) = nil error, want a rejection", tc.raw)
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
		if _, err := parseCustomHeaders(raw); err == nil {
			t.Errorf("parseCustomHeaders(%q) = nil error, want a rejection", raw)
		}
	}
}

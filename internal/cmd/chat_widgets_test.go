package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/viz"
)

// Setup errors decide between falling back silently, a retryable failure, and
// a canceled chat.
func TestWidgetsFailure(t *testing.T) {
	for err, want := range map[error]viz.Status{
		context.Canceled: {Phase: viz.Canceled},
		errors.Join(errUnsupportedSite, errors.New("staging host")): {Phase: viz.Unavailable},
		errors.New("network down"):                                  {Phase: viz.Failed, Retry: viz.RetryAllowed},
	} {
		if got := widgetsFailure(err); got.Phase != want.Phase || got.Retry != want.Retry {
			t.Errorf("%v: %+v, want %+v", err, got, want)
		}
	}
}

func TestFrameURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.datadoghq.com":            "https://api.datadoghq.com/api/unstable/dataviz-mcp-ui",
		"https://api.datadoghq.com/v1?q=1#top": "https://api.datadoghq.com/api/unstable/dataviz-mcp-ui",
		"https://api.example.org":              "",
		"http://api.datadoghq.com":             "",
		"https://user@api.datadoghq.com":       "",
	} {
		got, err := frameURL(base)
		if got != want || (want == "") != errors.Is(err, errUnsupportedSite) {
			t.Errorf("%s: %q, %v; want %q", base, got, err, want)
		}
	}
}

func TestVizRenderer(t *testing.T) {
	for value, want := range map[string]string{"": "auto", "AUTO": "auto", " snapshot ": "snapshot", "Off": "off", "none": ""} {
		if got, err := vizRenderer(value); got != want || (err != nil) != (want == "") {
			t.Errorf("%q: %q, %v; want %q", value, got, err, want)
		}
	}
}

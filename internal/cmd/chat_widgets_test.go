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

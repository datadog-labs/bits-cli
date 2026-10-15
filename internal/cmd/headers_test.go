package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/auth"
)

// A malformed configuration must be reported before any authentication work,
// so it is not hidden behind a login prompt and cannot be preceded by a token
// refresh that persists new credentials for an invocation that cannot run.
func TestCustomHeadersValidatedBeforeAuthentication(t *testing.T) {
	t.Setenv(EnvCustomHeaders, "bad name=1")
	// ModeAuto with no credential store would otherwise fail on the store first.
	_, err := NewAuthenticatedClient(context.Background(), ClientOptions{Mode: auth.ModeAuto})
	if err == nil {
		t.Fatal("expected the malformed configuration to be refused")
	}
	if !strings.Contains(err.Error(), EnvCustomHeaders) {
		t.Errorf("error = %v, want the %s validation error", err, EnvCustomHeaders)
	}
}

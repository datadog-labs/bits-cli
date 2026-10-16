package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/config"
)

// A malformed configuration must be reported before any authentication work,
// so it is not hidden behind a login prompt and cannot be preceded by a token
// refresh that persists new credentials for an invocation that cannot run.
func TestCustomHeadersValidatedBeforeAuthentication(t *testing.T) {
	t.Setenv(config.EnvCustomHeaders, "bad name=1")
	// The default auto mode would otherwise reach the OAuth credential store.
	for _, args := range [][]string{
		{"run", "--prompt", "x", "--delivery", "adeep"},
		{"login"},
	} {
		out, stderr, err := executeForTest(t, args)
		if err == nil || !strings.Contains(err.Error(), config.EnvCustomHeaders) {
			t.Fatalf("Execute(%q) error = %v, want the %s validation error", args, err, config.EnvCustomHeaders)
		}
		var exit *ExitError
		if !errors.As(err, &exit) || exit.Code != ExitUsage {
			t.Fatalf("Execute(%q) error = %#v, want usage exit", args, err)
		}
		if out != "" || stderr != "" {
			t.Fatalf("Execute(%q) wrote output %q, stderr %q before refusing", args, out, stderr)
		}
	}
}

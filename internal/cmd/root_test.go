package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/site"
)

func clearStagingEnv(t *testing.T) {
	t.Helper()
	t.Setenv(site.EnvStagingSite, "")
	t.Setenv(site.EnvStagingDomain, "")
	t.Setenv(site.EnvStagingClientID, "")
}

func executeForTest(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = Execute(context.Background(), args, &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestCommandHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"login", "--help"}, {"run", "--help"}, {"--version"}} {
		out, stderr, err := executeForTest(t, args)
		if err != nil || out == "" || stderr != "" {
			t.Fatalf("Execute(%q) = output %q, stderr %q, error %v", args, out, stderr, err)
		}
	}
	out, _, _ := executeForTest(t, []string{"--help"})
	for _, want := range []string{"login", "logout", "run", "--auth", "--permissions"} {
		if !strings.Contains(out, want) {
			t.Errorf("root help missing %q", want)
		}
	}
}

func TestCommandUsageErrors(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"loign"}, `unknown command "loign"`},
		{[]string{"--unknown"}, "unknown flag: --unknown"},
		{[]string{"login", "--auth", "api-key"}, "unknown flag: --auth"},
		{[]string{"--auth", "oauth"}, "invalid authentication mode"},
		{[]string{"--auth", "api-key"}, "--auth api-key requires --site"},
		{[]string{"--site", "app.datadoghq.eu"}, "--site requires --auth api-key"},
		{[]string{"--permissions", "allow-all"}, "invalid permissions mode"},
		{[]string{"run", "--delivery", "adeep"}, `required flag "--prompt" was not set`},
		{[]string{"run", "--prompt", " ", "--delivery", "adeep"}, "--prompt must not be empty"},
		{[]string{"run", "--prompt", "x"}, `required flag "--delivery" was not set`},
		{[]string{"run", "--prompt", "x", "--delivery", "plain"}, "invalid delivery"},
		{[]string{"run", "--prompt", "x", "--delivery", "adeep", "--permissions", "manual"}, "invalid permissions mode"},
		{[]string{"run", "--prompt", "x", "--delivery", "adeep", "--reasoning", "normal"}, "invalid inference mode"},
		{[]string{"run", "--prompt", "x", "--delivery", "adeep", "--image", "/nonexistent/shot.png"}, `could not attach "/nonexistent/shot.png"`},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			out, stderr, err := executeForTest(t, tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != ExitUsage {
				t.Fatalf("error = %v, want usage exit", err)
			}
			if out != "" || stderr != "" {
				t.Fatalf("usage output = %q, stderr = %q", out, stderr)
			}
		})
	}
}

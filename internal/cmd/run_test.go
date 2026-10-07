package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/site"
)

// clearStagingEnv clears inherited staging configuration for one test; empty
// values are equivalent to unset for the staging loader.
func clearStagingEnv(t *testing.T) {
	t.Helper()
	t.Setenv(site.EnvStagingSite, "")
	t.Setenv(site.EnvStagingDomain, "")
	t.Setenv(site.EnvStagingClientID, "")
}

func runOptionsForTest(err error) (Actions, *[]RunOptions) {
	var recorded []RunOptions
	return Actions{
		Run: func(_ context.Context, opts RunOptions) error {
			recorded = append(recorded, opts)
			return err
		},
	}, &recorded
}

func executeRun(t *testing.T, args []string, actionErr error) (stdout, stderr string, recorded []RunOptions, err error) {
	t.Helper()
	actions, runRecorder := runOptionsForTest(actionErr)
	var out bytes.Buffer
	var errOut bytes.Buffer
	err = Execute(context.Background(), args, actions, &out, &errOut)
	return out.String(), errOut.String(), *runRecorder, err
}

func TestRunDispatchesResolvedOptions(t *testing.T) {
	clearStagingEnv(t)
	stdout, stderr, recorded, err := executeRun(t, []string{
		"run",
		"--prompt", "summarize the incident",
		"--delivery", "adeep",
		"--model", "claude-sonnet-4-6",
		"--reasoning", "fast",
		"--auth", "api-key",
		"--site", "https://api.datadoghq.eu",
		"--conversation", "conversation-9",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("run printed outside the delivery: stdout %q, stderr %q", stdout, stderr)
	}
	want := []RunOptions{{
		ChatOptions: ChatOptions{
			ConversationID:  "conversation-9",
			Model:           "claude-sonnet-4-6",
			InferenceMode:   "fast",
			AuthMode:        auth.ModeAPIKey,
			Site:            "https://api.datadoghq.eu",
			PermissionsMode: agent.ModeDeny,
		},
		Prompt:   "summarize the incident",
		Delivery: "adeep",
	}}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("run options = %#v, want %#v", recorded, want)
	}
}

// The api-key CLI path canonicalizes a configured staging login origin to
// the environment's API host before dispatching the run.
func TestRunAPIKeyCanonicalizesStagingLoginOrigin(t *testing.T) {
	clearStagingEnv(t)
	t.Setenv(site.EnvStagingSite, "https://login.staging.test")
	t.Setenv(site.EnvStagingDomain, "staging.test")
	_, _, recorded, err := executeRun(t, []string{
		"run", "--prompt", "hello", "--delivery", "adeep",
		"--auth", "api-key", "--site", "https://login.staging.test",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0].Site != "https://api.staging.test" {
		t.Fatalf("run options = %#v, want the canonical staging API site", recorded)
	}
}

func TestRunDefaultsToDeny(t *testing.T) {
	_, _, recorded, err := executeRun(t, []string{"run", "--prompt", "hello", "--delivery", "adeep"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 {
		t.Fatalf("runs = %d, want 1", len(recorded))
	}
	opts := recorded[0]
	if opts.AuthMode != auth.ModeAuto || opts.PermissionsMode != agent.ModeDeny || opts.Site != "" || opts.ConversationID != "" || opts.Model != "" || opts.InferenceMode != "" {
		t.Fatalf("defaults = %#v, want deny with the shared chat defaults", opts.ChatOptions)
	}
}

// Every usage failure must be detected before the action (and therefore any
// engine construction) runs, and must carry the usage exit status.
func TestRunRejectsUsageBeforeAction(t *testing.T) {
	clearStagingEnv(t)
	for _, test := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "missing prompt", args: []string{"run", "--delivery", "adeep"}, wantErr: `required flag "--prompt" was not set`},
		{name: "empty prompt", args: []string{"run", "--prompt", "", "--delivery", "adeep"}, wantErr: "--prompt must not be empty or whitespace"},
		{name: "whitespace prompt", args: []string{"run", "--prompt", "  \t ", "--delivery", "adeep"}, wantErr: "--prompt must not be empty or whitespace"},
		{name: "stdin prompt", args: []string{"run", "--prompt", "-", "--delivery", "adeep"}, wantErr: `--prompt must be a literal message; stdin ("-") is not supported`},
		{name: "positional prompt", args: []string{"run", "do things", "--prompt", "x", "--delivery", "adeep"}, wantErr: `unknown command "do things" for "bits run"`},
		{name: "missing delivery", args: []string{"run", "--prompt", "hello"}, wantErr: `required flag "--delivery" was not set`},
		{name: "unknown delivery", args: []string{"run", "--prompt", "hello", "--delivery", "plain"}, wantErr: `invalid delivery "plain"; expected adeep`},
		{name: "removed approval flag", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--approval", "allow-all"}, wantErr: "unknown flag: --approval"},
		{name: "manual permissions", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--permissions", "manual"}, wantErr: `invalid permissions mode "manual"; expected deny or skip-permissions`},
		{name: "invalid auth mode", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--auth", "oauth"}, wantErr: `invalid authentication mode "oauth"; expected auto or api-key`},
		{name: "invalid reasoning mode", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--reasoning", "normal"}, wantErr: `invalid inference mode "normal"; expected fast or deep`},
		{name: "api key without site", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--auth", "api-key"}, wantErr: "--auth api-key requires --site"},
		{name: "web console site", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--auth", "api-key", "--site", "https://app.datadoghq.com"}, wantErr: "datadog API site must use an api-prefixed hostname"},
		{name: "site without api key", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--site", "app.datadoghq.eu"}, wantErr: "--site requires --auth api-key"},
		{name: "unknown run flag", args: []string{"run", "--prompt", "x", "--delivery", "adeep", "--unknown"}, wantErr: "unknown flag: --unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, recorded, err := executeRun(t, test.args, nil)
			if err == nil {
				t.Fatalf("Execute(%q) succeeded", test.args)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, test.wantErr)
			}
			var exitErr *ExitError
			if !errors.As(err, &exitErr) || exitErr.Code != ExitUsage {
				t.Fatalf("error = %v, want an ExitError with code %d", err, ExitUsage)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("usage failure printed output: stdout %q, stderr %q", stdout, stderr)
			}
			if len(recorded) != 0 {
				t.Fatalf("usage failure dispatched the action: %#v", recorded)
			}
		})
	}
}

// Usage errors of the pre-existing commands keep the same text and now also
// carry the usage status.
func TestUsageErrorsCarryUsageExitStatus(t *testing.T) {
	recorder := &commandRecorder{}
	_, _, err := executeForTest(t, []string{"loign"}, recorder)
	if err == nil {
		t.Fatal("misspelled command succeeded")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitUsage {
		t.Fatalf("error = %v, want usage status", err)
	}
	if len(recorder.chats) != 0 {
		t.Fatal("usage error dispatched chat")
	}
}

func TestRunActionErrorKeepsItsOwnStatus(t *testing.T) {
	// An action error is dispatched work failing, not usage: the denial status
	// from the action must survive the Execute boundary untouched.
	_, _, _, err := executeRun(t, []string{"run", "--prompt", "x", "--delivery", "adeep"}, &ExitError{Code: ExitApprovalDenied, Err: errors.New("denied")})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitApprovalDenied {
		t.Fatalf("error = %v, want ExitError code %d", err, ExitApprovalDenied)
	}
}

func TestRootHelpListsRun(t *testing.T) {
	recorder := &commandRecorder{}
	stdout, _, err := executeForTest(t, []string{"--help"}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run", "Run one noninteractive assistant turn"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("root help missing %q:\n%s", want, stdout)
		}
	}
}

func TestVersionFlagPrintsUsefulVersion(t *testing.T) {
	recorder := &commandRecorder{}
	stdout, stderr, err := executeForTest(t, []string{"--version"}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "bits version ") || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("version output = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("version wrote to stderr: %q", stderr)
	}
	if len(recorder.chats) != 0 || len(recorder.logins) != 0 || recorder.logouts != 0 {
		t.Fatalf("--version dispatched an action: %#v", recorder)
	}
}

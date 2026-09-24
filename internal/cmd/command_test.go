package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/auth"
)

type commandRecorder struct {
	chats   []ChatOptions
	logins  []LoginOptions
	logouts int
	err     error
}

func (r *commandRecorder) actionsWithValues() Actions {
	return Actions{
		Chat: func(_ context.Context, opts ChatOptions) error {
			r.chats = append(r.chats, opts)
			return r.err
		},
		Login: func(_ context.Context, opts LoginOptions) error {
			r.logins = append(r.logins, opts)
			return r.err
		},
		Logout: func(context.Context) error {
			r.logouts++
			return r.err
		},
	}
}

func executeForTest(t *testing.T, args []string, recorder *commandRecorder) (stdout, stderr string, err error) {
	t.Helper()
	var out bytes.Buffer
	var errOut bytes.Buffer
	err = Execute(context.Background(), args, recorder.actionsWithValues(), &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestCommandDispatch(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		chats     []ChatOptions
		logins    []LoginOptions
		logoutCnt int
	}{
		{
			name:  "chat",
			chats: []ChatOptions{{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual}},
		},
		{
			name:  "chat conversation",
			args:  []string{"--conversation", "conversation-1"},
			chats: []ChatOptions{{ConversationID: "conversation-1", AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual}},
		},
		{
			name:  "chat conversation equals",
			args:  []string{"--conversation=conversation-2"},
			chats: []ChatOptions{{ConversationID: "conversation-2", AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual}},
		},
		{
			name: "explicit API key mode",
			args: []string{"--auth", "api-key", "--site=https://api.datadoghq.eu"},
			chats: []ChatOptions{{
				AuthMode:        auth.ModeAPIKey,
				Site:            "https://api.datadoghq.eu",
				PermissionsMode: agent.ModeManual,
			}},
		},
		{
			name:  "permissions default manual",
			args:  []string{"--permissions=manual"},
			chats: []ChatOptions{{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual}},
		},
		{
			name:  "skip-permissions opt-in",
			args:  []string{"--permissions", "skip-permissions"},
			chats: []ChatOptions{{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeSkipPermissions}},
		},
		{
			name:   "login defaults",
			args:   []string{"login"},
			logins: []LoginOptions{{Site: auth.DefaultSite}},
		},
		{
			name:   "login flags",
			args:   []string{"login", "--site", "app.datadoghq.eu", "--client-id=explicit-client"},
			logins: []LoginOptions{{Site: "app.datadoghq.eu", ClientID: "explicit-client"}},
		},
		{name: "logout", args: []string{"logout"}, logoutCnt: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &commandRecorder{}
			stdout, stderr, err := executeForTest(t, test.args, recorder)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("unexpected command output: stdout %q, stderr %q", stdout, stderr)
			}
			if !reflect.DeepEqual(recorder.chats, test.chats) || !reflect.DeepEqual(recorder.logins, test.logins) || recorder.logouts != test.logoutCnt {
				t.Fatalf("calls = chat %#v, login %#v, logout %d", recorder.chats, recorder.logins, recorder.logouts)
			}
		})
	}
}

func TestCommandTreesDoNotShareFlagState(t *testing.T) {
	recorder := &commandRecorder{}
	if _, _, err := executeForTest(t, []string{"--auth", "api-key", "--site", "api.datadoghq.eu", "--permissions", "skip-permissions"}, recorder); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeForTest(t, nil, recorder); err != nil {
		t.Fatal(err)
	}
	want := []ChatOptions{
		{AuthMode: auth.ModeAPIKey, Site: "api.datadoghq.eu", PermissionsMode: agent.ModeSkipPermissions},
		{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual},
	}
	if !reflect.DeepEqual(recorder.chats, want) {
		t.Fatalf("chat options = %#v, want %#v", recorder.chats, want)
	}
}

func TestCommandHelp(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		want      []string
		doNotWant []string
	}{
		{
			name: "root short",
			args: []string{"-h"},
			want: []string{
				"Datadog Assistant in your terminal", "Available Commands:",
				"login", "logout", "--auth", "auto or api-key", "--permissions", "manual or skip-permissions", "--site",
				"--conversation", "--help",
			},
			doNotWant: []string{"completion", "--client-id"},
		},
		{
			name: "root long",
			args: []string{"--help"},
			want: []string{"Available Commands:", "login", "logout", "--auth", "--permissions", "--site"},
		},
		{
			name: "help login",
			args: []string{"help", "login"},
			want: []string{"Sign in to Datadog", "--site", "--client-id"},
		},
		{
			name: "login help",
			args: []string{"login", "--help"},
			want: []string{"Sign in to Datadog", "--site", "--client-id"},
		},
		{
			name: "logout help",
			args: []string{"logout", "-h"},
			want: []string{"Sign out of Datadog", "Usage:", "bits logout"},
		},
		{
			name: "unknown help topic",
			args: []string{"help", "bogus"},
			want: []string{"Unknown help topic", "bogus", "Usage:", "Available Commands:"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &commandRecorder{}
			stdout, stderr, err := executeForTest(t, test.args, recorder)
			if err != nil {
				t.Fatal(err)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q", stderr)
			}
			for _, want := range test.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("help output missing %q:\n%s", want, stdout)
				}
			}
			for _, notWant := range test.doNotWant {
				if strings.Contains(stdout, notWant) {
					t.Errorf("help output unexpectedly contains %q:\n%s", notWant, stdout)
				}
			}
			if len(recorder.chats) != 0 || len(recorder.logins) != 0 || recorder.logouts != 0 {
				t.Fatalf("help invoked an action: %#v", recorder)
			}
		})
	}
}

func TestCommandRejectsInvalidInputWithoutInvokingActions(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "root positional", args: []string{"unexpected"}, wantErr: `unknown command "unexpected" for "bits"`},
		{name: "misspelled command", args: []string{"loign"}, wantErr: `unknown command "loign" for "bits"`},
		{name: "disabled completion", args: []string{"completion"}, wantErr: `unknown command "completion" for "bits"`},
		{name: "argument after terminator", args: []string{"--", "login"}, wantErr: `unknown command "login" for "bits"`},
		{name: "login positional", args: []string{"login", "unexpected"}, wantErr: `unknown command "unexpected" for "bits login"`},
		{name: "logout positional", args: []string{"logout", "unexpected"}, wantErr: `unknown command "unexpected" for "bits logout"`},
		{name: "unknown root flag", args: []string{"--unknown"}, wantErr: "unknown flag: --unknown"},
		{name: "unknown login flag", args: []string{"login", "--unknown"}, wantErr: "unknown flag: --unknown"},
		{name: "root auth flag on login", args: []string{"login", "--auth", "api-key"}, wantErr: "unknown flag: --auth"},
		{name: "root flag on login", args: []string{"login", "--conversation", "conversation-1"}, wantErr: "unknown flag: --conversation"},
		{name: "invalid authentication mode", args: []string{"--auth", "oauth"}, wantErr: `invalid authentication mode "oauth"; expected auto or api-key`},
		{name: "invalid permissions mode lists valid modes", args: []string{"--permissions", "yes"}, wantErr: `invalid permissions mode "yes"; expected manual or skip-permissions`},
		{name: "gated value rejected", args: []string{"--permissions", "gated"}, wantErr: `invalid permissions mode "gated"; expected manual or skip-permissions`},
		{name: "allow-all value rejected", args: []string{"--permissions", "allow-all"}, wantErr: `invalid permissions mode "allow-all"; expected manual or skip-permissions`},
		{name: "removed approval flag is unknown", args: []string{"--approval", "gated"}, wantErr: "unknown flag: --approval"},
		{name: "permissions flag on login", args: []string{"login", "--permissions", "manual"}, wantErr: "unknown flag: --permissions"},
		{name: "single dash permissions", args: []string{"-permissions=manual"}, wantErr: "unknown shorthand flag: 'p'"},
		{name: "API key mode without site", args: []string{"--auth", "api-key"}, wantErr: "--auth api-key requires --site"},
		{name: "API key mode with empty site", args: []string{"--auth", "api-key", "--site", " "}, wantErr: "--auth api-key requires --site"},
		{name: "site in automatic mode", args: []string{"--site", "app.datadoghq.eu"}, wantErr: "--site requires --auth api-key"},
		{name: "OAuth client ID on root", args: []string{"--client-id", "client"}, wantErr: "unknown flag: --client-id"},
		{name: "single dash conversation", args: []string{"-conversation", "conversation-1"}},
		{name: "single dash conversation equals", args: []string{"-conversation=conversation-1"}, wantErr: "unknown shorthand flag: 'c'"},
		{name: "single dash auth", args: []string{"-auth=api-key"}, wantErr: "unknown shorthand flag: 'a'"},
		{name: "single dash site", args: []string{"-site=app.datadoghq.com"}, wantErr: "unknown shorthand flag: 's'"},
		{name: "single dash client id", args: []string{"-client-id=client"}, wantErr: "unknown shorthand flag: 'c'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &commandRecorder{}
			stdout, stderr, err := executeForTest(t, test.args, recorder)
			if err == nil {
				t.Fatalf("Execute(%q) succeeded", test.args)
			}
			if test.wantErr != "" && !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, test.wantErr)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("invalid command printed output: stdout %q, stderr %q", stdout, stderr)
			}
			if len(recorder.chats) != 0 || len(recorder.logins) != 0 || recorder.logouts != 0 {
				t.Fatalf("invalid command invoked an action: %#v", recorder)
			}
		})
	}
}

func TestCommandSuggestsCloseMatches(t *testing.T) {
	recorder := &commandRecorder{}
	_, _, err := executeForTest(t, []string{"loign"}, recorder)
	if err == nil {
		t.Fatal("misspelled command succeeded")
	}
	for _, want := range []string{`unknown command "loign" for "bits"`, "Did you mean this?", "login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %q", want, err)
		}
	}
}

func TestFlagErrorsPointToCommandHelp(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: []string{"--unknown"}, want: "Run 'bits --help' for usage"},
		{args: []string{"login", "--unknown"}, want: "Run 'bits login --help' for usage"},
	} {
		recorder := &commandRecorder{}
		_, _, err := executeForTest(t, test.args, recorder)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Execute(%q) error = %q, want it to contain %q", test.args, err, test.want)
		}
	}
}

func TestCommandPropagatesContextAndActionErrors(t *testing.T) {
	type contextKey string
	const key contextKey = "test"
	ctx := context.WithValue(context.Background(), key, "value")
	wantErr := errors.New("action failed")
	var gotContext context.Context
	actions := Actions{
		Chat: func(ctx context.Context, _ ChatOptions) error {
			gotContext = ctx
			return wantErr
		},
		Login:  func(context.Context, LoginOptions) error { return nil },
		Logout: func(context.Context) error { return nil },
	}
	var stdout, stderr bytes.Buffer
	err := Execute(ctx, nil, actions, &stdout, &stderr)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if gotContext == nil || gotContext.Value(key) != "value" {
		t.Fatalf("action context value = %v", gotContext)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("action error printed before main boundary: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/DataDog/bits-cli/internal/auth"
)

type commandRecorder struct {
	chatIDs []string
	logins  []LoginOptions
	logouts int
	err     error
}

func (r *commandRecorder) actionsWithValues() Actions {
	return Actions{
		Chat: func(_ context.Context, conversationID string) error {
			r.chatIDs = append(r.chatIDs, conversationID)
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

func executeForTest(t *testing.T, args []string, recorder *commandRecorder, defaults Defaults) (stdout, stderr string, err error) {
	t.Helper()
	var out bytes.Buffer
	var errOut bytes.Buffer
	err = Execute(context.Background(), args, recorder.actionsWithValues(), defaults, &out, &errOut)
	return out.String(), errOut.String(), err
}

func TestDefaultLoginSite(t *testing.T) {
	if got := defaultLoginSite(""); got != auth.DefaultSite {
		t.Fatalf("defaultLoginSite(\"\") = %q, want %q", got, auth.DefaultSite)
	}
	if got := defaultLoginSite(auth.DefaultStagingSite); got != auth.DefaultStagingSite {
		t.Fatalf("defaultLoginSite(staging) = %q, want configured site", got)
	}
}

func TestCommandDispatch(t *testing.T) {
	defaults := Defaults{Site: "https://env.datad0g.com", ClientID: "env-client"}
	for _, test := range []struct {
		name      string
		args      []string
		chatIDs   []string
		logins    []LoginOptions
		logoutCnt int
	}{
		{name: "chat", chatIDs: []string{""}},
		{name: "chat conversation", args: []string{"--conversation", "conversation-1"}, chatIDs: []string{"conversation-1"}},
		{name: "chat conversation equals", args: []string{"--conversation=conversation-2"}, chatIDs: []string{"conversation-2"}},
		{name: "login defaults", args: []string{"login"}, logins: []LoginOptions{{Site: defaults.Site, ClientID: defaults.ClientID}}},
		{name: "login flags", args: []string{"login", "--site", "app.datadoghq.eu", "--client-id=explicit-client"}, logins: []LoginOptions{{Site: "app.datadoghq.eu", ClientID: "explicit-client"}}},
		{name: "logout", args: []string{"logout"}, logoutCnt: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &commandRecorder{}
			stdout, stderr, err := executeForTest(t, test.args, recorder, defaults)
			if err != nil {
				t.Fatal(err)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("unexpected command output: stdout %q, stderr %q", stdout, stderr)
			}
			if !reflect.DeepEqual(recorder.chatIDs, test.chatIDs) || !reflect.DeepEqual(recorder.logins, test.logins) || recorder.logouts != test.logoutCnt {
				t.Fatalf("calls = chat %#v, login %#v, logout %d", recorder.chatIDs, recorder.logins, recorder.logouts)
			}
		})
	}
}

func TestLoginDefaults(t *testing.T) {
	for _, test := range []struct {
		name     string
		defaults Defaults
		want     LoginOptions
	}{
		{
			name: "production site",
			want: LoginOptions{Site: auth.DefaultSite},
		},
		{
			name:     "environment overrides",
			defaults: Defaults{Site: auth.DefaultStagingSite, ClientID: "client-from-env"},
			want:     LoginOptions{Site: auth.DefaultStagingSite, ClientID: "client-from-env"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &commandRecorder{}
			_, _, err := executeForTest(t, []string{"login"}, recorder, test.defaults)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(recorder.logins, []LoginOptions{test.want}) {
				t.Fatalf("login options = %#v, want %#v", recorder.logins, test.want)
			}
		})
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
			name:      "root short",
			args:      []string{"-h"},
			want:      []string{"Datadog Assistant in your terminal", "Available Commands:", "login", "logout", "--conversation", "--help"},
			doNotWant: []string{"completion"},
		},
		{
			name: "root long",
			args: []string{"--help"},
			want: []string{"Available Commands:", "login", "logout"},
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
			stdout, stderr, err := executeForTest(t, test.args, recorder, Defaults{})
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
			if len(recorder.chatIDs) != 0 || len(recorder.logins) != 0 || recorder.logouts != 0 {
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
		{name: "argument after terminator", args: []string{"--", "login"}, wantErr: `unknown command "login" for "bits"`},
		{name: "login positional", args: []string{"login", "unexpected"}, wantErr: `unknown command "unexpected" for "bits login"`},
		{name: "logout positional", args: []string{"logout", "unexpected"}, wantErr: `unknown command "unexpected" for "bits logout"`},
		{name: "unknown root flag", args: []string{"--unknown"}, wantErr: "unknown flag: --unknown"},
		{name: "unknown login flag", args: []string{"login", "--unknown"}, wantErr: "unknown flag: --unknown"},
		{name: "root flag on login", args: []string{"login", "--conversation", "conversation-1"}, wantErr: "unknown flag: --conversation"},
		{name: "single dash conversation", args: []string{"-conversation", "conversation-1"}},
		{name: "single dash conversation equals", args: []string{"-conversation=conversation-1"}, wantErr: "unknown shorthand flag: 'c'"},
		{name: "single dash site", args: []string{"login", "-site", "app.datadoghq.com"}, wantErr: "unknown shorthand flag: 's'"},
		{name: "single dash client id", args: []string{"login", "-client-id", "client"}, wantErr: "unknown shorthand flag: 'c'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &commandRecorder{}
			stdout, stderr, err := executeForTest(t, test.args, recorder, Defaults{})
			if err == nil {
				t.Fatalf("Execute(%q) succeeded", test.args)
			}
			if test.wantErr != "" && !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, test.wantErr)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("invalid command printed output: stdout %q, stderr %q", stdout, stderr)
			}
			if len(recorder.chatIDs) != 0 || len(recorder.logins) != 0 || recorder.logouts != 0 {
				t.Fatalf("invalid command invoked an action: %#v", recorder)
			}
		})
	}
}

func TestCommandSuggestsCloseMatches(t *testing.T) {
	recorder := &commandRecorder{}
	_, _, err := executeForTest(t, []string{"loign"}, recorder, Defaults{})
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
		_, _, err := executeForTest(t, test.args, recorder, Defaults{})
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
		Chat: func(ctx context.Context, _ string) error {
			gotContext = ctx
			return wantErr
		},
		Login:  func(context.Context, LoginOptions) error { return nil },
		Logout: func(context.Context) error { return nil },
	}
	var stdout, stderr bytes.Buffer
	err := Execute(ctx, nil, actions, Defaults{}, &stdout, &stderr)
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

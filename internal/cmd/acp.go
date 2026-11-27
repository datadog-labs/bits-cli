package cmd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/spf13/cobra"

	"github.com/datadog-labs/bits-cli/internal/acp"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/tools"
	"github.com/datadog-labs/bits-cli/internal/version"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// loginFlag makes `bits acp` sign in and exit: an editor runs it in a
// terminal as the agent's own command line plus this flag.
const loginFlag = "login"

// newACPCommand serves the Agent Client Protocol on stdin and stdout, for
// editors that embed bits. stdout carries only the protocol; diagnostics go
// to stderr.
func newACPCommand(invoked *bool, stdout, stderr io.Writer) *cobra.Command {
	var login bool
	site := auth.DefaultSite
	command := &cobra.Command{
		Use:   "acp",
		Short: "Serve the Agent Client Protocol on stdio, for editors such as Zed",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			*invoked = true
			if login {
				return runLogin(command.Context(), LoginOptions{Site: site}, stderr)
			}
			return runACP(command.Context(), auth.DefaultStore(), site, os.Stdin, stdout, stderr)
		},
	}
	command.Flags().BoolVar(&login, loginFlag, false, "sign in to Datadog, then exit")
	// An editor appends its login arguments to the configured ones, so a
	// --site in the agent's arguments reaches the login too.
	command.Flags().StringVar(&site, "site", site, "Datadog site URL or hostname to sign in to")
	return command
}

func runACP(ctx context.Context, store auth.CredentialStore, site string, in io.Reader, out, stderr io.Writer) error {
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	opts := ChatOptions{AuthMode: auth.ModeAuto, PermissionsMode: agent.ModeManual}
	backendFor := func(ctx context.Context) (agent.Backend, error) { return newBackend(ctx, opts, store) }
	if os.Getenv("BITS_FAKE_BACKEND") == "1" {
		// One fake for every session, so one loads what another prompted.
		shared, err := newBackend(ctx, opts, store)
		if err != nil {
			return err
		}
		backendFor = func(context.Context) (agent.Backend, error) { return shared, nil }
	}
	config := acp.Config{
		Log: log, Version: version.Build(), LoginArgs: []string{"--" + loginFlag},
		Open: func(ctx context.Context, conversationID, cwd string, ui *tools.UI) (acp.Env, error) {
			b, err := backendFor(ctx)
			if err != nil {
				return acp.Env{}, err
			}
			return openACPSession(ctx, b, opts, conversationID, cwd, ui, log)
		},
		Error:  acpError,
		SignIn: func(ctx context.Context) error { return signIn(ctx, store, site, stderr) },
	}
	if executable, err := os.Executable(); err != nil {
		log.Warn("no login command for clients without terminal auth", "err", err)
	} else {
		config.Login = []string{executable, "login", "--site", site}
	}
	return acp.NewAgent(config).Serve(ctx, out, in)
}

// signIn opens the browser to sign in, unless the store holds a login, as
// after a terminal login. The address goes to stderr, the editor's log:
// stdin is the protocol, so a callback address cannot be pasted.
func signIn(ctx context.Context, store auth.CredentialStore, site string, stderr io.Writer) error {
	if _, err := store.Load(); err == nil {
		return nil
	}
	_, err := auth.Login(ctx, auth.LoginOptions{Site: site, Store: store, Out: stderr})
	return err
}

// acpError makes a login the session needs, at its start or later, the
// client's auth_required: a stored login can be revoked while the session
// runs. A login replaced since the session opened is not one: the session
// refuses any login but its own, so signing in again cannot help.
func acpError(err error) error {
	if errors.Is(err, auth.ErrSessionReplaced) {
		return errors.New("the login changed since this thread opened: reopen the thread to use the new login")
	}
	if errors.Is(classifyOAuthError(err), ErrLoginRequired) {
		return acpsdk.NewAuthRequired(map[string]any{"message": err.Error()})
	}
	return err
}

// openACPSession builds an editor session's workspace, commands, tools and
// engine at cwd, as the chat builds them at the working directory.
func openACPSession(ctx context.Context, b agent.Backend, opts ChatOptions, conversationID, cwd string, ui *tools.UI, log *slog.Logger) (acp.Env, error) {
	ws, err := workspace.Open(cwd)
	if err != nil {
		return acp.Env{}, err
	}
	closeWorkspace := func() {
		if err := ws.Close(); err != nil {
			log.Warn("close workspace", "path", cwd, "err", err)
		}
	}
	commands, err := newExecService(ws)
	if err != nil {
		closeWorkspace()
		return acp.Env{}, err
	}
	release := func() {
		// The session is over only once no command it started is running.
		commands.Close()
		closeWorkspace()
	}
	list := tools.NewClientTools(ws, commands)
	if ui != nil {
		list = append(list, tools.NewAskUserQuestionTool(ui))
	}
	toolSet, err := agent.NewToolSet(list...)
	if err != nil {
		release()
		return acp.Env{}, err
	}
	opts.ConversationID = conversationID
	config, err := engineConfig(opts, ws)
	if err != nil {
		release()
		return acp.Env{}, err
	}
	engine, err := newEngineOn(ctx, b, config, ws)
	if err != nil {
		release()
		return acp.Env{}, err
	}
	return acp.Env{Engine: engine, Tools: toolSet, Close: release}, nil
}

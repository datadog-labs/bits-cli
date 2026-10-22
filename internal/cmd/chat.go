package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/tools"
	"github.com/datadog-labs/bits-cli/internal/tui"
	loginui "github.com/datadog-labs/bits-cli/internal/tui/login"
	"github.com/datadog-labs/bits-cli/internal/update"
	"github.com/datadog-labs/bits-cli/internal/version"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

func printLoggedOut(w io.Writer) error {
	_, err := fmt.Fprintln(w, "Logged out of Bits.")
	return err
}

func runChat(parent context.Context, opts ChatOptions, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	checker := update.New()
	workspace, err := openCurrentWorkspace()
	if err != nil {
		return err
	}
	defer func() { _ = workspace.Close() }()
	root, err := startupModel(ctx, opts, workspace, checker.Offer(ctx, !opts.NoUpdateCheck))
	if err != nil {
		return err
	}
	defer root.Detach()

	// Login and chat are modes of one Bubble Tea program. Keeping the same
	// renderer alive prevents an alt-screen teardown flash after OAuth succeeds.
	model, err := tea.NewProgram(root, tea.WithContext(ctx)).Run()
	// The chat may leave an OSC 22 hand pointer over a clickable row. Reset it
	// once the program has stopped, so every exit path (ctrl+c, /quit, /logout,
	// errors) is covered. Terminals without OSC 22 ignore it.
	_, _ = fmt.Fprint(stdout, ansi.SetPointerShape("default"))
	if err != nil {
		return err
	}
	m, ok := model.(*tui.Model)
	if !ok {
		return errors.New("bits TUI returned an unexpected model")
	}
	if err := m.StartupError(); err != nil {
		if errors.Is(err, loginui.ErrCanceled) {
			return fmt.Errorf("sign in to Datadog: %w", err)
		}
		return err
	}
	if m.LoggedOut() {
		return printLoggedOut(stderr)
	}
	// The installer runs first so its output cannot scroll the resume hint away.
	var upgradeErr error
	if m.UpgradeRequested() {
		upgradeErr = checker.Upgrade(os.Stdin, stdout, stderr)
	}
	// On a clean exit with an active conversation, surface how to get back to it.
	// The conversation id is the server-side handle the Assistant API restores via
	// --conversation; there is no local session store yet.
	if err := printResumeHint(stdout, m.ConversationID()); err != nil {
		return err
	}
	return upgradeErr
}

// printResumeHint prints the resume line only for a backend-shaped conversation id.
func printResumeHint(w io.Writer, conversationID string) error {
	if !agent.ValidConversationID(conversationID) {
		return nil
	}
	_, err := fmt.Fprintf(w, "Resume this conversation with: bits --conversation %s\n", conversationID)
	return err
}

func startupModel(ctx context.Context, opts ChatOptions, workspace *workspace.Workspace, upgrade *update.Offer) (*tui.Model, error) {
	return startupModelWithStore(ctx, opts, auth.DefaultStore(), workspace, upgrade)
}

func startupModelWithStore(ctx context.Context, opts ChatOptions, store auth.CredentialStore, workspace *workspace.Workspace, upgrade *update.Offer) (*tui.Model, error) {
	toolUI := tools.NewUI()
	clientTools, err := configuredClientTools(workspace)
	if err != nil {
		return nil, err
	}
	clientTools = append(clientTools, tools.NewAskUserQuestionTool(toolUI), tools.NewHandoffTool())
	toolSet, err := agent.NewToolSet(clientTools...)
	if err != nil {
		return nil, err
	}
	config := tui.Config{
		Tools:     toolSet,
		ToolUI:    toolUI,
		Version:   version.Build(),
		Workspace: workspace,
		Upgrade:   upgrade,
		Logout: func(logoutCtx context.Context) (bool, error, error) {
			return auth.Logout(logoutCtx, store, nil)
		},
	}
	// An engine lives as long as the chat: runChat cancels ctx on every exit
	// path, including logout and errors, which closes it.
	bound := func(engine *agent.Engine, err error) (*agent.Engine, error) {
		if err == nil {
			context.AfterFunc(ctx, engine.Close)
		}
		return engine, err
	}
	engine, err := bound(newEngine(ctx, opts, workspace, store))
	if err == nil {
		return tui.New(engine, config), nil
	}
	if !errors.Is(err, ErrLoginRequired) {
		return nil, err
	}

	loginModel := loginui.New(ctx, func(loginCtx context.Context, site string, callbacks <-chan string, report func(loginui.BrowserStatus)) error {
		_, loginErr := auth.Login(loginCtx, auth.LoginOptions{
			Site:         site,
			ClientID:     "",
			Store:        store,
			CallbackURLs: callbacks,
			OnCallbackError: func(err error) {
				report(loginui.BrowserStatus{InputError: err})
			},
			OnBrowserOpen: func(url string, openErr error) {
				report(loginui.BrowserStatus{AuthorizationURL: url, OpenError: openErr})
			},
			Out: io.Discard,
		})
		return loginErr
	})
	return tui.NewWithLogin(ctx, loginModel, func(factoryCtx context.Context) (*agent.Engine, error) {
		return bound(newEngine(factoryCtx, opts, workspace, store))
	}, config), nil
}

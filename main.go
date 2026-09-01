package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/cmd"
	"github.com/DataDog/bits-cli/internal/startup"
	"github.com/DataDog/bits-cli/internal/tools"
	"github.com/DataDog/bits-cli/internal/tui"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bits:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	return cmd.Execute(
		context.Background(),
		args,
		cmd.Actions{
			Chat:   runChat,
			Login:  runLogin,
			Logout: runLogout,
		},
		os.Stdout,
		os.Stderr,
	)
}

func runLogin(ctx context.Context, opts cmd.LoginOptions) error {
	session, err := auth.Login(ctx, auth.LoginOptions{
		Site:     opts.Site,
		ClientID: opts.ClientID,
		Store:    auth.DefaultStore(),
		Out:      os.Stderr,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in to %s.\n", session.Site)
	return nil
}

func runLogout(ctx context.Context) error {
	logoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	hadSession, revokeErr, err := auth.Logout(logoutCtx, auth.DefaultStore(), nil)
	cancel()
	if err != nil {
		return err
	}
	if !hadSession {
		fmt.Fprintln(os.Stderr, "No Bits CLI OAuth session is stored.")
		return nil
	}
	if revokeErr != nil {
		fmt.Fprintf(os.Stderr, "Logged out locally; remote token revocation failed: %v\n", revokeErr)
		return nil
	}
	fmt.Fprintln(os.Stderr, "Logged out.")
	return nil
}

func runChat(parent context.Context, opts cmd.ChatOptions) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	root, err := startupModel(ctx, opts)
	if err != nil {
		return err
	}

	// Login and chat are modes of one Bubble Tea program. Keeping the same
	// renderer alive prevents an alt-screen teardown flash after OAuth succeeds.
	model, err := tea.NewProgram(root, tea.WithContext(ctx)).Run()
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
	// On a clean exit with an active conversation, surface how to get back to it.
	// The conversation id is the server-side handle the Assistant API restores via
	// --conversation; there is no local session store yet.
	if m.ConversationID() != "" {
		fmt.Printf("Resume this conversation with: bits --conversation %s\n", m.ConversationID())
	}
	return nil
}

func startupModel(ctx context.Context, opts cmd.ChatOptions) (*tui.Model, error) {
	return startupModelWithStore(ctx, opts, auth.DefaultStore())
}

func startupModelWithStore(ctx context.Context, opts cmd.ChatOptions, store auth.CredentialStore) (*tui.Model, error) {
	workspaceRoot, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine workspace: %w", err)
	}
	workspaceTools, err := tools.NewEditorTools(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	toolSet, err := agent.NewToolSet(opts.ApprovalMode, workspaceTools...)
	if err != nil {
		return nil, err
	}
	config := tui.Config{Tools: toolSet}
	engineOpts := startup.EngineOptions{
		Client: startup.ClientOptions{
			Mode:    opts.AuthMode,
			Store:   store,
			APIKey:  os.Getenv("DD_API_KEY"),
			AppKey:  os.Getenv("DD_APP_KEY"),
			APISite: opts.Site,
		},
		Send:           assistant.SendOptions{ConversationID: opts.ConversationID},
		UseFakeBackend: os.Getenv("BITS_FAKE_BACKEND") == "1",
	}
	engine, err := startup.NewEngine(ctx, engineOpts)
	if err == nil {
		return tui.New(engine, config), nil
	}
	if !errors.Is(err, startup.ErrLoginRequired) {
		return nil, err
	}

	loginModel := loginui.New(ctx, func(loginCtx context.Context, site string, report func(loginui.BrowserStatus)) error {
		_, loginErr := auth.Login(loginCtx, auth.LoginOptions{
			Site:     site,
			ClientID: "",
			Store:    store,
			OnBrowserOpen: func(url string, openErr error) {
				report(loginui.BrowserStatus{AuthorizationURL: url, OpenError: openErr})
			},
			Out: io.Discard,
		})
		return loginErr
	})
	return tui.NewWithLogin(ctx, loginModel, func(factoryCtx context.Context) (*agent.Engine, error) {
		return startup.NewEngine(factoryCtx, engineOpts)
	}, config), nil
}

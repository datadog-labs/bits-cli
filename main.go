package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/tui"
	loginui "github.com/DataDog/bits-cli/internal/tui/login"
)

var errNoWorkingAuth = errors.New("no working authentication")

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bits:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "login":
			return runLogin(args[1:])
		case "logout":
			return runLogout(args[1:])
		}
	}
	return runChat(args)
}

func runLogin(args []string) error {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	defaultSite := defaultLoginSite(os.Getenv("DD_SITE_URL"))
	site := flags.String("site", defaultSite, "Datadog site URL or hostname")
	clientID := flags.String("client-id", os.Getenv("BITS_OAUTH_CLIENT_ID"), "OAuth client ID override")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("login does not accept positional arguments")
	}

	session, err := auth.Login(context.Background(), auth.LoginOptions{
		Site:     *site,
		ClientID: *clientID,
		Store:    auth.DefaultStore(),
		Out:      os.Stderr,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in to %s.\n", session.Site)
	return nil
}

func defaultLoginSite(configuredSite string) string {
	if configuredSite != "" {
		return configuredSite
	}
	return auth.DefaultSite
}

func runLogout(args []string) error {
	flags := flag.NewFlagSet("logout", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("logout does not accept positional arguments")
	}

	logoutCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

func runChat(args []string) error {
	flags := flag.NewFlagSet("bits", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	conversationID := flags.String("conversation", "",
		"resume an existing conversation by id: its history is restored before the prompt")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := startupModel(ctx, *conversationID)
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

func startupModel(ctx context.Context, conversationID string) (*tui.Model, error) {
	options := assistant.SendOptions{ConversationID: conversationID}
	// BITS_FAKE_BACKEND streams seeded pseudo-random output with no network or
	// auth, for offline development and demos.
	if os.Getenv("BITS_FAKE_BACKEND") == "1" {
		return tui.New(agent.New(fake.New(), options)), nil
	}

	store := auth.DefaultStore()
	apiKey := os.Getenv("DD_API_KEY")
	appKey := os.Getenv("DD_APP_KEY")
	apiSite := os.Getenv("DD_SITE_URL")
	clientID := strings.TrimSpace(os.Getenv("BITS_OAUTH_CLIENT_ID"))
	client, err := authenticatedClientWithContext(ctx, store, apiKey, appKey, apiSite)
	if err == nil {
		return tui.New(agent.New(client, options)), nil
	}
	if !canStartLogin(err) {
		return nil, err
	}

	// An explicitly configured site retains the non-picker OAuth path. With no
	// override, startup login is embedded in the root TUI and hands off in place.
	if configuredSite := strings.TrimSpace(apiSite); configuredSite != "" {
		client, err = ensureAuthenticatedClient(ctx, store, apiKey, appKey, apiSite, func() error {
			_, loginErr := auth.Login(ctx, auth.LoginOptions{
				Site:     configuredSite,
				ClientID: clientID,
				Store:    store,
				Out:      os.Stderr,
			})
			return loginErr
		})
		if err != nil {
			return nil, err
		}
		return tui.New(agent.New(client, options)), nil
	}

	loginModel := loginui.New(ctx, func(loginCtx context.Context, site string, report func(loginui.BrowserStatus)) error {
		_, loginErr := auth.Login(loginCtx, auth.LoginOptions{
			Site:     site,
			ClientID: clientID,
			Store:    store,
			OnBrowserOpen: func(url string, openErr error) {
				report(loginui.BrowserStatus{AuthorizationURL: url, OpenError: openErr})
			},
			Out: io.Discard,
		})
		return loginErr
	}, clientID)
	return tui.NewWithLogin(ctx, loginModel, func(factoryCtx context.Context) (*agent.Engine, error) {
		client, factoryErr := authenticatedClientWithContext(factoryCtx, store, apiKey, appKey, apiSite)
		if factoryErr != nil {
			return nil, factoryErr
		}
		return agent.New(client, options), nil
	}), nil
}

func ensureAuthenticatedClient(
	ctx context.Context,
	store auth.CredentialStore,
	apiKey, appKey, apiSite string,
	login func() error,
) (*assistant.Client, error) {
	client, err := authenticatedClientWithContext(ctx, store, apiKey, appKey, apiSite)
	if err == nil {
		return client, nil
	}

	// A corrupt or definitively unrefreshable OAuth session is not working auth.
	// Login can safely replace either one; transient keyring/network failures are
	// surfaced instead of unexpectedly opening a browser.
	if !canStartLogin(err) {
		return nil, err
	}
	if err := login(); err != nil {
		return nil, fmt.Errorf("sign in to Datadog: %w", err)
	}
	return authenticatedClientWithContext(ctx, store, apiKey, appKey, apiSite)
}

func canStartLogin(err error) bool {
	return errors.Is(err, errNoWorkingAuth) ||
		errors.Is(err, auth.ErrSessionCorrupt) ||
		errors.Is(err, auth.ErrReauthRequired)
}

func authenticatedClientWithContext(ctx context.Context, store auth.CredentialStore, apiKey, appKey, apiSite string) (*assistant.Client, error) {
	// A stored OAuth login is the customer path and deliberately wins over
	// ambient developer credentials. Complete API/app-key pairs are only the
	// CI/developer fallback when no OAuth session exists. Real keyring errors
	// must surface rather than silently switching principals.
	session, err := store.Load()
	if err == nil {
		source, sourceErr := auth.NewSource(session, store, nil)
		if sourceErr != nil {
			// The credential decoded but its required fields or trusted Datadog
			// routing are unusable. Treat this deterministic local failure like any
			// other corrupt stored session so startup login can safely replace it.
			return nil, fmt.Errorf("%w: %w", auth.ErrSessionCorrupt, sourceErr)
		}
		// Validate the grant before opening the chat UI. This is a local check for
		// a fresh token and performs the existing safe refresh transaction only
		// when the token is near expiry.
		if _, sourceErr = source.AccessToken(ctx); sourceErr != nil {
			return nil, sourceErr
		}
		return assistant.NewOAuthClient(source.Site(), source)
	}
	if errors.Is(err, auth.ErrSessionCorrupt) {
		return nil, fmt.Errorf("stored Bits login is unreadable; run `bits logout`, then `bits login`: %w", err)
	}
	if !errors.Is(err, auth.ErrNoSession) {
		return nil, err
	}
	if apiKey != "" && appKey != "" {
		return assistant.NewAPIKeyClient(apiSite, apiKey, appKey)
	}
	return nil, fmt.Errorf("%w: no OAuth session or complete API/app-key pair", errNoWorkingAuth)
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/tui"
)

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
	defaultSite := os.Getenv("DD_SITE_URL")
	if defaultSite == "" {
		defaultSite = auth.DefaultStagingSite
	}
	site := flags.String("site", defaultSite, "Datadog site URL or hostname")
	clientID := flags.String("client-id", os.Getenv("BITS_OAUTH_CLIENT_ID"), "OAuth client ID override")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("login does not accept positional arguments")
	}

	session, err := auth.Login(context.Background(), auth.LoginOptions{
		Site:     *site,
		ClientID: *clientID,
		Store:    auth.KeyringStore{},
		Out:      os.Stderr,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Logged in to %s.\n", session.Site)
	return nil
}

func runLogout(args []string) error {
	flags := flag.NewFlagSet("logout", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("logout does not accept positional arguments")
	}

	store := auth.KeyringStore{}
	session, err := store.Load()
	if errors.Is(err, auth.ErrNoSession) {
		fmt.Fprintln(os.Stderr, "No Bits CLI OAuth session is stored.")
		return nil
	}
	if err != nil {
		return err
	}

	revokeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	revokeErr := auth.Revoke(revokeCtx, session, nil)
	cancel()
	if err := store.Delete(); err != nil {
		return err
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
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	// BITS_FAKE_BACKEND streams seeded pseudo-random output with no network or
	// auth, for offline development and demos.
	var backend agent.Backend
	if os.Getenv("BITS_FAKE_BACKEND") == "1" {
		backend = fake.New()
	} else {
		client, err := authenticatedClient()
		if err != nil {
			return err
		}
		backend = client
	}

	engine := agent.New(backend, assistant.SendOptions{ConversationID: *conversationID})
	p := tea.NewProgram(tui.New(engine))
	model, err := p.Run()
	if err != nil {
		return err
	}
	// On a clean exit with an active conversation, surface how to get back to it.
	// The conversation id is the server-side handle the Assistant API restores via
	// --conversation; there is no local session store yet.
	if m, ok := model.(*tui.Model); ok && m.ConversationID() != "" {
		fmt.Printf("Resume this conversation with: bits --conversation %s\n", m.ConversationID())
	}
	return nil
}

func authenticatedClient() (*assistant.Client, error) {
	apiKey, appKey := os.Getenv("DD_API_KEY"), os.Getenv("DD_APP_KEY")
	if apiKey != "" && appKey != "" {
		return assistant.NewClient()
	}

	store := auth.KeyringStore{}
	session, err := store.Load()
	if errors.Is(err, auth.ErrNoSession) {
		return nil, fmt.Errorf("not logged in; run `bits login --site %s`", auth.DefaultStagingSite)
	}
	if err != nil {
		return nil, err
	}
	source, err := auth.NewSource(session, store, nil)
	if err != nil {
		return nil, err
	}
	return assistant.NewOAuthClient(source.Site(), source)
}

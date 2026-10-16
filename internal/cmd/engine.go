package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// ErrLoginRequired marks an OAuth state that an interactive caller may replace
// by starting login. Noninteractive callers should surface the wrapped error
// with guidance to run `bits login`.
var ErrLoginRequired = errors.New("OAuth login is required")

// newEngine constructs an engine backed by either the explicit fake backend or
// an authenticated Assistant client. Explicit API-key mode takes precedence
// over the fake backend so an invalid automation invocation cannot silently
// become a demo session.
func newEngine(ctx context.Context, opts ChatOptions, workspace *workspace.Workspace, store auth.CredentialStore) (*agent.Engine, error) {
	cfg := opts.Config
	config := agent.Config{ConversationID: opts.ConversationID, Model: cfg.Model, InferenceMode: cfg.InferenceMode}
	if workspace != nil {
		config.ProjectInstructions = env.NewInstructions(workspace.Path())
		config.ClientSkills = env.NewSkills(workspace.Path(), opts.SkillPaths)
		config.UserContext = env.UserContext(workspace)
	}

	switch opts.AuthMode {
	case auth.ModeAuto:
		if cfg.FakeBackend {
			backend := fake.New()
			// The fake also takes a script for --conversation: it becomes the
			// first turn of a conversation an earlier session left behind.
			if script := config.ConversationID; script != "" && !agent.ValidConversationID(script) {
				id, err := backend.PushConversation(ctx, "", script)
				if err != nil {
					return nil, fmt.Errorf("fake --conversation script: %w", err)
				}
				config.ConversationID = id
			}
			return agent.New(backend, config), nil
		}
	case auth.ModeAPIKey:
		// Continue below; API-key mode must validate its explicit credentials.
	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", opts.AuthMode)
	}

	client, err := newClient(ctx, opts, store)
	if err != nil {
		if opts.AuthMode == auth.ModeAPIKey {
			return nil, fmt.Errorf("authenticate with API/app keys: %w", err)
		}
		return nil, err
	}
	return agent.New(client, config), nil
}

// newClient constructs exactly the client selected by opts.AuthMode and
// configured by opts.Config. Automatic mode is OAuth-only; ambient API/app
// keys never change principals.
func newClient(ctx context.Context, opts ChatOptions, store auth.CredentialStore) (*assistant.Client, error) {
	client, err := newClientForMode(ctx, opts, store)
	if err != nil {
		return nil, err
	}
	client.CustomHeaders = opts.Config.CustomHeaders
	// Both constructors leave BaseURL as the canonical https://<host>.
	client.WebHost, _ = auth.WebHostForAssistantHost(strings.TrimPrefix(client.BaseURL, "https://"))
	return client, nil
}

func newClientForMode(ctx context.Context, opts ChatOptions, store auth.CredentialStore) (*assistant.Client, error) {
	cfg := opts.Config
	switch opts.AuthMode {
	case auth.ModeAPIKey:
		if strings.TrimSpace(opts.Site) == "" {
			return nil, fmt.Errorf("api-key authentication requires a Datadog site")
		}
		normalizedSite, err := auth.NormalizeAPISite(opts.Site)
		if err != nil {
			return nil, fmt.Errorf("validate API-key Datadog site: %w", err)
		}
		return assistant.NewAPIKeyClient(normalizedSite, cfg.APIKey(), cfg.AppKey())

	case auth.ModeAuto:
		if store == nil {
			return nil, fmt.Errorf("OAuth credential store is required")
		}
		session, err := store.Load()
		if err != nil {
			return nil, classifyOAuthError(err)
		}
		source, err := auth.NewSource(session, store, nil)
		if err != nil {
			// The stored session can also be unusable for structural reasons, so
			// never advise deleting credentials here.
			return nil, fmt.Errorf("%w: cannot use stored OAuth session; run `bits login`: %w: %w", ErrLoginRequired, auth.ErrSessionCorrupt, err)
		}
		// Validate the grant before handing the client to a surface. This is a
		// local check for a fresh token and performs the existing safe refresh
		// transaction only when the token is near expiry.
		if _, err = source.AccessToken(ctx); err != nil {
			return nil, classifyOAuthError(err)
		}
		return assistant.NewOAuthClient(source.Site(), source)

	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", opts.AuthMode)
	}
}

func classifyOAuthError(err error) error {
	switch {
	case errors.Is(err, auth.ErrNoSession):
		return fmt.Errorf("%w: no OAuth session; run `bits login`: %w", ErrLoginRequired, err)
	case errors.Is(err, auth.ErrSessionCorrupt):
		return fmt.Errorf("%w: stored Bits login is unreadable; run `bits logout`, then `bits login`: %w", ErrLoginRequired, err)
	case errors.Is(err, auth.ErrReauthRequired):
		return fmt.Errorf("%w: run `bits login`: %w", ErrLoginRequired, err)
	default:
		return err
	}
}

func openCurrentWorkspace() (*workspace.Workspace, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine workspace: %w", err)
	}
	return workspace.Open(root)
}

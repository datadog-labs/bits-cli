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
	"github.com/datadog-labs/bits-cli/internal/tools"
	"github.com/datadog-labs/bits-cli/internal/workspace"
)

// EnvCustomHeaders carries extra request headers as a comma-separated list of
// name=value pairs, applied to every Assistant API request:
//
//	BITS_CUSTOM_HEADERS='x-route-hint=1,x-trace-me=abc'
//
// It is deliberately an environment variable rather than a flag: the mechanism
// is for development and support, and is not part of the command-line contract.
// See assistant.ParseCustomHeaders for the accepted syntax.
const EnvCustomHeaders = "BITS_CUSTOM_HEADERS"

// EnvExecExcludeEnv lists variables to omit from agent shell subprocesses.
// When unset, shell commands inherit the ordinary process environment.
const EnvExecExcludeEnv = "BITS_EXEC_EXCLUDE_ENV"

// ErrLoginRequired marks an OAuth state that an interactive caller may replace
// by starting login. Noninteractive callers should surface the wrapped error
// with guidance to run `bits login`.
var ErrLoginRequired = errors.New("OAuth login is required")

// ClientOptions selects and configures one authentication path.
type ClientOptions struct {
	Mode    auth.Mode
	Store   auth.CredentialStore
	APIKey  string
	AppKey  string
	APISite string
}

// newEngine constructs an engine backed by either the explicit fake backend or
// an authenticated Assistant client. Explicit API-key mode takes precedence
// over the fake backend so an invalid automation invocation cannot silently
// become a demo session.
func newEngine(ctx context.Context, opts ChatOptions, workspace *workspace.Workspace, store auth.CredentialStore) (*agent.Engine, error) {
	config := agent.Config{ConversationID: opts.ConversationID, Model: opts.Model, InferenceMode: opts.InferenceMode}
	if workspace != nil {
		config.ProjectInstructions = env.NewInstructions(workspace.Path())
		config.ClientSkills = env.NewSkills(workspace.Path(), opts.SkillPaths)
		config.UserContext = env.UserContext(workspace)
	}

	switch opts.AuthMode {
	case auth.ModeAuto:
		if os.Getenv("BITS_FAKE_BACKEND") == "1" {
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

	client, err := NewAuthenticatedClient(ctx, ClientOptions{
		Mode:    opts.AuthMode,
		Store:   store,
		APIKey:  os.Getenv("DD_API_KEY"),
		AppKey:  os.Getenv("DD_APP_KEY"),
		APISite: opts.Site,
	})
	if err != nil {
		if opts.AuthMode == auth.ModeAPIKey {
			return nil, fmt.Errorf("authenticate with API/app keys: %w", err)
		}
		return nil, err
	}
	return agent.New(client, config), nil
}

func configuredClientTools(ws *workspace.Workspace) ([]agent.Tool, error) {
	raw := os.Getenv(EnvExecExcludeEnv)
	if raw == "" {
		return tools.NewClientTools(ws), nil
	}
	names, err := parseExecExcludedNames(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvExecExcludeEnv, err)
	}
	return tools.NewClientTools(ws, append(names, EnvExecExcludeEnv)...), nil
}

func parseExecExcludedNames(raw string) ([]string, error) {
	var names []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if !validExecEnvName(name) {
			return nil, fmt.Errorf("invalid environment variable name")
		}
		names = append(names, name)
	}
	return names, nil
}

func validExecEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// NewAuthenticatedClient constructs exactly the client selected by opts.Mode.
// Automatic mode is OAuth-only; ambient API/app keys never change principals.
func NewAuthenticatedClient(ctx context.Context, opts ClientOptions) (*assistant.Client, error) {
	// Before authenticating: a malformed configuration is then reported as
	// itself, rather than behind a login prompt, and without a token refresh
	// having already persisted new credentials for an invocation that cannot run.
	custom, err := assistant.ParseCustomHeaders(os.Getenv(EnvCustomHeaders))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvCustomHeaders, err)
	}
	client, err := newClientForMode(ctx, opts)
	if err != nil {
		return nil, err
	}
	client.CustomHeaders = custom
	return client, nil
}

func newClientForMode(ctx context.Context, opts ClientOptions) (*assistant.Client, error) {
	switch opts.Mode {
	case auth.ModeAPIKey:
		if strings.TrimSpace(opts.APISite) == "" {
			return nil, fmt.Errorf("api-key authentication requires a Datadog site")
		}
		normalizedSite, err := auth.NormalizeAPISite(opts.APISite)
		if err != nil {
			return nil, fmt.Errorf("validate API-key Datadog site: %w", err)
		}
		return assistant.NewAPIKeyClient(normalizedSite, opts.APIKey, opts.AppKey)

	case auth.ModeAuto:
		if opts.Store == nil {
			return nil, fmt.Errorf("OAuth credential store is required")
		}
		session, err := opts.Store.Load()
		if err != nil {
			return nil, classifyOAuthError(err)
		}
		source, err := auth.NewSource(session, opts.Store, nil)
		if err != nil {
			// A site-configuration failure (for example a staging login whose
			// BITS_STAGING_* variables are missing or no longer match) is
			// actionable as written, so it surfaces without the login hint; a
			// caller must not replace it with the production login picker.
			if errors.Is(err, auth.ErrSiteConfiguration) {
				return nil, fmt.Errorf("cannot use stored OAuth session; restore the matching site configuration or run `bits login`: %w", err)
			}
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
		return nil, fmt.Errorf("unsupported authentication mode %q", opts.Mode)
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

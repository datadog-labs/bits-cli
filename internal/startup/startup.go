// Package startup constructs authenticated Assistant clients and agent engines
// without depending on a terminal UI.
package startup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DataDog/bits-cli/internal/agent"
	"github.com/DataDog/bits-cli/internal/agent/fake"
	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
)

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

// EngineOptions configures a surface-independent agent engine.
type EngineOptions struct {
	Client         ClientOptions
	Send           assistant.SendOptions
	UseFakeBackend bool
}

// defaultSkillOverrides keep Bits on its local-workspace tool path rather than
// activating the remote code skills, and disable skills that depend on active
// web browser pages or client-side UI tools (such as Canvas or the DDSQL editor).
// They are sent with every Assistant request, including client-tool
// continuations, by the engine. This policy could move to the backend CLI
// profile once it is appropriate for every CLI client, but stays here for now
// so the harness enforces it independently.
var defaultSkillOverrides = []assistant.SkillOverride{
	{Name: "canvas-builder", Source: assistant.SkillSourceAssistant, Enabled: false},            // Requires an open browser Canvas and its client tools.
	{Name: "code-investigation", Source: assistant.SkillSourceAssistant, Enabled: false},        // Uses the remote Code Sandbox instead of the local workspace.
	{Name: "code-sandbox", Source: assistant.SkillSourceAssistant, Enabled: false},              // Provides remote sandbox file and execution tools unavailable in Bits.
	{Name: "code-search", Source: assistant.SkillSourceDatadogMCP, Enabled: false},              // Keeps repository investigation on Bits' local workspace tools.
	{Name: "coding", Source: assistant.SkillSourceAssistant, Enabled: false},                    // Starts remote coding sessions rather than editing locally.
	{Name: "dashboard-builder", Source: assistant.SkillSourceAssistant, Enabled: false},         // Requires an open dashboard page and browser client tools.
	{Name: "exploring-commit-history", Source: assistant.SkillSourceAssistant, Enabled: false},  // Uses remote code-search history instead of the local Git checkout.
	{Name: "generate_ddsql_query", Source: assistant.SkillSourceAssistant, Enabled: false},      // Targets the browser DDSQL editor and its mutation tools.
	{Name: "update-cloudcraft-diagram", Source: assistant.SkillSourceAssistant, Enabled: false}, // Requires the currently open Cloudcraft diagram page.
}

// NewEngine constructs an engine backed by either the explicit fake backend or
// an authenticated Assistant client. Explicit API-key mode takes precedence
// over the fake backend so an invalid automation invocation cannot silently
// become a demo session.
func NewEngine(ctx context.Context, opts EngineOptions) (*agent.Engine, error) {
	opts.Send = withDefaultSkillOverrides(opts.Send)
	switch opts.Client.Mode {
	case auth.ModeAuto:
		if opts.UseFakeBackend {
			backend := fake.New()
			// The fake also takes a script for --conversation: it becomes the
			// first turn of a conversation an earlier session left behind.
			if script := opts.Send.ConversationID; script != "" && !agent.ValidConversationID(script) {
				id, err := backend.PushConversation(ctx, "", script)
				if err != nil {
					return nil, fmt.Errorf("fake --conversation script: %w", err)
				}
				opts.Send.ConversationID = id
			}
			return agent.New(backend, opts.Send), nil
		}
	case auth.ModeAPIKey:
		// Continue below; API-key mode must validate its explicit credentials.
	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", opts.Client.Mode)
	}

	client, err := NewAuthenticatedClient(ctx, opts.Client)
	if err != nil {
		if opts.Client.Mode == auth.ModeAPIKey {
			return nil, fmt.Errorf("authenticate with API/app keys: %w", err)
		}
		return nil, err
	}
	return agent.New(client, opts.Send), nil
}

// withDefaultSkillOverrides supplies this client's disabled-by-default skills
// unless the caller explicitly configured that same skill. Explicit options
// therefore remain an escape hatch for a future opt-in surface.
func withDefaultSkillOverrides(opts assistant.SendOptions) assistant.SendOptions {
	overrides := append([]assistant.SkillOverride(nil), opts.SkillOverrides...)
	configured := make(map[skillKey]struct{}, len(overrides))
	for _, override := range overrides {
		configured[skillKey{name: override.Name, source: override.Source}] = struct{}{}
	}
	for _, override := range defaultSkillOverrides {
		key := skillKey{name: override.Name, source: override.Source}
		if _, ok := configured[key]; ok {
			continue
		}
		overrides = append(overrides, override)
	}
	opts.SkillOverrides = overrides
	return opts
}

type skillKey struct {
	name   string
	source assistant.SkillSource
}

// NewAuthenticatedClient constructs exactly the client selected by opts.Mode.
// Automatic mode is OAuth-only; ambient API/app keys never change principals.
func NewAuthenticatedClient(ctx context.Context, opts ClientOptions) (*assistant.Client, error) {
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
			return nil, fmt.Errorf("%w: stored OAuth session is invalid; run `bits logout`, then `bits login`: %w: %w", ErrLoginRequired, auth.ErrSessionCorrupt, err)
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

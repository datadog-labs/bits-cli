package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/agent/env"
	"github.com/datadog-labs/bits-cli/internal/agent/fake"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/browser"
	exectool "github.com/datadog-labs/bits-cli/internal/tools/exec"
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

// EnvRequiredResponseHeader requires a matching header before streamed output
// is delivered. It uses the same name=value syntax as EnvCustomHeaders.
const EnvRequiredResponseHeader = "BITS_REQUIRED_RESPONSE_HEADER"

// EnvProfile overrides the assistant profile, for development: the server
// sends some content, such as MCP apps, to the web_ui profile only.
const EnvProfile = "BITS_PROFILE"

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

// conversationFromArg reads --conversation, which takes an ID or a link whose
// last path segment is one, such as the link /share copies.
func conversationFromArg(arg string) string {
	if u, err := url.Parse(strings.TrimSpace(arg)); err == nil && u.Scheme != "" {
		if id := path.Base(u.Path); agent.ValidConversationID(id) {
			return id
		}
	}
	return arg
}

// newEngine constructs an engine for workspace on the backend opts selects.
// Its configuration is checked first, before authenticating, as
// NewAuthenticatedClient checks its own.
func newEngine(ctx context.Context, opts ChatOptions, workspace *workspace.Workspace, store auth.CredentialStore) (*agent.Engine, error) {
	config, err := engineConfig(opts, workspace)
	if err != nil {
		return nil, err
	}
	b, err := newBackend(ctx, opts, store)
	if err != nil {
		return nil, err
	}
	return newEngineOn(ctx, b, config, workspace)
}

// engineConfig is the engine configuration opts and workspace select, apart
// from what depends on the backend.
func engineConfig(opts ChatOptions, workspace *workspace.Workspace) (agent.Config, error) {
	config := agent.Config{
		ConversationID: conversationFromArg(opts.ConversationID), PermissionsMode: opts.PermissionsMode,
		Model: opts.Model, InferenceMode: opts.InferenceMode,
		Profile: assistant.Profile(os.Getenv(EnvProfile)),
	}
	if workspace != nil {
		config.ProjectInstructions = env.NewInstructions(workspace.Path())
		skillDirs, err := env.SkillDirs(workspace.Path(), opts.SkillPaths)
		if err != nil {
			return agent.Config{}, fmt.Errorf("--skill: %w", err)
		}
		config.ClientSkills = env.NewSkills(workspace.Path(), skillDirs)
	}
	return config, nil
}

// newBackend returns either the explicit fake backend or an authenticated
// Assistant client. Explicit API-key mode takes precedence over the fake
// backend so an invalid automation invocation cannot silently become a demo
// session.
func newBackend(ctx context.Context, opts ChatOptions, store auth.CredentialStore) (agent.Backend, error) {
	switch opts.AuthMode {
	case auth.ModeAuto:
		if os.Getenv("BITS_FAKE_BACKEND") == "1" {
			return fake.New(), nil
		}
	case auth.ModeAPIKey:
		// Continue below; API-key mode must validate its explicit credentials.
	default:
		return nil, fmt.Errorf("unsupported authentication mode %q", opts.AuthMode)
	}
	client, err := NewAuthenticatedClient(ctx, clientOptions(opts, store))
	if err != nil {
		if opts.AuthMode == auth.ModeAPIKey {
			return nil, fmt.Errorf("authenticate with API/app keys: %w", err)
		}
		return nil, err
	}
	if useSubagents() {
		return assistant.NewMultiAgentClient(client), nil
	}
	return client, nil
}

// newEngineOn constructs an engine for workspace on b, from config.
func newEngineOn(ctx context.Context, b agent.Backend, config agent.Config, workspace *workspace.Workspace) (*agent.Engine, error) {
	// The fake reports no web origin, so its context links nowhere.
	var webOrigin string
	if status, ok := b.(agent.StatusBackend); ok {
		webOrigin = status.BackendStatus().WebOrigin
	}
	config.UserContext = env.UserContext(workspace, webOrigin)
	// The fake also takes a script for --conversation: it becomes the first
	// turn of a conversation an earlier session left behind. An id opens an
	// empty one, so scripts can show that real conversation's saved apps.
	if f, ok := b.(*fake.Fake); ok {
		switch script := config.ConversationID; {
		case agent.ValidConversationID(script):
			f.AdoptConversation(script)
		case script != "":
			id, err := f.PushConversation(ctx, "", script)
			if err != nil {
				return nil, fmt.Errorf("fake --conversation script: %w", err)
			}
			config.ConversationID = id
		}
	}
	return agent.New(b, config), nil
}

func useSubagents() bool {
	return os.Getenv("BITS_USE_EXPERIMENTAL_SUBAGENTS") == "1"
}

// newExecService returns the service that runs every command of this
// process. The caller closes it on exit to stop commands still running.
func newExecService(ws *workspace.Workspace) (*exectool.ExecService, error) {
	raw := os.Getenv(EnvExecExcludeEnv)
	if raw == "" {
		return exectool.NewExecService(ws.DefaultShellPath()), nil
	}
	names, err := parseExecExcludedNames(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvExecExcludeEnv, err)
	}
	return exectool.NewExecService(ws.DefaultShellPath(), append(names, EnvExecExcludeEnv)...), nil
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

// clientOptions selects the chat's credentials, shared by every client the
// chat authenticates.
func clientOptions(opts ChatOptions, store auth.CredentialStore) ClientOptions {
	return ClientOptions{
		Mode:    opts.AuthMode,
		Store:   store,
		APIKey:  os.Getenv("DD_API_KEY"),
		AppKey:  os.Getenv("DD_APP_KEY"),
		APISite: opts.Site,
	}
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
	var requiredName, requiredValue string
	if raw := os.Getenv(EnvRequiredResponseHeader); raw != "" {
		requiredName, requiredValue, err = assistant.ParseRequiredResponseHeader(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", EnvRequiredResponseHeader, err)
		}
	}
	client, err := newClientForMode(ctx, opts)
	if err != nil {
		return nil, err
	}
	if client.WebOrigin == "" {
		client.WebOrigin, _ = browser.WebOrigin(client.BaseURL)
	}
	client.CustomHeaders = custom
	client.RequiredStreamHeaderName = requiredName
	client.RequiredStreamHeaderValue = requiredValue
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
		source, err := auth.NewSource(ctx, opts.Store, nil)
		if err != nil {
			// A site-configuration failure (for example a staging login whose
			// BITS_STAGING_* variables are missing or no longer match) is
			// actionable as written, so it surfaces without the login hint; a
			// caller must not replace it with the production login picker.
			if errors.Is(err, auth.ErrSiteConfiguration) {
				return nil, fmt.Errorf("cannot use stored OAuth session; restore the matching site configuration or run `bits login`: %w", err)
			}
			return nil, classifyOAuthError(err)
		}
		// Validate the grant before handing the client to a surface. This is a
		// local check for a fresh token and performs the existing safe refresh
		// transaction only when the token is near expiry.
		if _, err = source.AccessToken(ctx); err != nil {
			return nil, classifyOAuthError(err)
		}
		client, err := assistant.NewOAuthClient(source.Site(), source)
		if err != nil {
			return nil, err
		}
		client.WebOrigin = source.WebOrigin()
		return client, nil

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

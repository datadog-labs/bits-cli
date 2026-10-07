package startup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/site"
)

// clearStagingEnv clears inherited staging configuration for one test; empty
// values are equivalent to unset for the staging loader. Unit tests stay
// deterministic under any configuration inherited from the developer's shell.
func clearStagingEnv(t *testing.T) {
	t.Helper()
	t.Setenv(site.EnvStagingSite, "")
	t.Setenv(site.EnvStagingDomain, "")
	t.Setenv(site.EnvStagingClientID, "")
}

type observedStore struct {
	loads   int
	saves   int
	deletes int
	session auth.Session
	err     error
}

func (s *observedStore) Load() (auth.Session, error) {
	s.loads++
	return s.session, s.err
}

func (s *observedStore) Save(session auth.Session) error {
	s.saves++
	s.session = session
	s.err = nil
	return nil
}

func (s *observedStore) Delete() error {
	s.deletes++
	s.session = auth.Session{}
	s.err = auth.ErrNoSession
	return nil
}

func validSession() auth.Session {
	return auth.Session{
		Site:         "https://api.datadoghq.com",
		ClientID:     "oauth-client",
		AccessToken:  "oauth-access",
		RefreshToken: "oauth-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
}

func TestNewAuthenticatedClientAPIKeyBypassesOAuthStore(t *testing.T) {
	clearStagingEnv(t)
	for _, store := range []*observedStore{
		{session: validSession()},
		{err: errors.New("keyring unavailable")},
	} {
		client, err := NewAuthenticatedClient(context.Background(), ClientOptions{
			Mode: auth.ModeAPIKey, Store: store, APIKey: "api-key", AppKey: "app-key", APISite: "api.datadoghq.eu",
		})
		if err != nil {
			t.Fatal(err)
		}
		if store.loads != 0 || store.saves != 0 || store.deletes != 0 {
			t.Fatalf("OAuth store operations = load %d, save %d, delete %d", store.loads, store.saves, store.deletes)
		}
		if client.TokenSource != nil || client.APIKey != "api-key" || client.AppKey != "app-key" || client.BaseURL != "https://api.datadoghq.eu" {
			t.Fatalf("API-key client = %#v", client)
		}
	}
}

func TestNewAuthenticatedClientAPIKeyRequiresSecretsAndTrustedSite(t *testing.T) {
	clearStagingEnv(t)
	for _, test := range []struct {
		name    string
		apiKey  string
		appKey  string
		site    string
		wantErr string
	}{
		{name: "missing keys", site: "api.datadoghq.com", wantErr: "DD_API_KEY and DD_APP_KEY must both be set"},
		{name: "partial keys", apiKey: "api-key", site: "api.datadoghq.com", wantErr: "DD_API_KEY and DD_APP_KEY must both be set"},
		{name: "missing site", apiKey: "api-key", appKey: "app-key", wantErr: "requires a Datadog site"},
		{name: "external site", apiKey: "api-key", appKey: "app-key", site: "https://example.com", wantErr: "Datadog-owned hostname"},
		{name: "login site", apiKey: "api-key", appKey: "app-key", site: "app.datadoghq.com", wantErr: "api-prefixed hostname"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &observedStore{session: validSession()}
			_, err := NewAuthenticatedClient(context.Background(), ClientOptions{
				Mode: auth.ModeAPIKey, Store: store, APIKey: test.apiKey, AppKey: test.appKey, APISite: test.site,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
			if store.loads != 0 || store.saves != 0 || store.deletes != 0 {
				t.Fatalf("OAuth store operations = load %d, save %d, delete %d", store.loads, store.saves, store.deletes)
			}
		})
	}
}

func TestNewAuthenticatedClientAutoUsesOAuthAndIgnoresAmbientKeys(t *testing.T) {
	clearStagingEnv(t)
	store := &observedStore{session: validSession()}
	client, err := NewAuthenticatedClient(context.Background(), ClientOptions{
		Mode: auth.ModeAuto, Store: store, APIKey: "ignored-api", AppKey: "ignored-app", APISite: "api.datadoghq.eu",
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.TokenSource == nil || client.APIKey != "" || client.AppKey != "" {
		t.Fatalf("client auth = token source %v, API key %q, app key %q", client.TokenSource != nil, client.APIKey, client.AppKey)
	}
	if client.BaseURL != "https://api.datadoghq.com" {
		t.Fatalf("BaseURL = %q", client.BaseURL)
	}
}

func TestNewAuthenticatedClientClassifiesOnlyReplaceableOAuthFailures(t *testing.T) {
	clearStagingEnv(t)
	for _, test := range []struct {
		name          string
		store         *observedStore
		wantCause     error
		loginRequired bool
	}{
		{name: "no session", store: &observedStore{err: auth.ErrNoSession}, wantCause: auth.ErrNoSession, loginRequired: true},
		{name: "corrupt session", store: &observedStore{err: errors.Join(auth.ErrSessionCorrupt, errors.New("truncated"))}, wantCause: auth.ErrSessionCorrupt, loginRequired: true},
		{name: "invalid decoded session", store: &observedStore{session: auth.Session{Site: "https://api.datadoghq.com"}}, wantCause: auth.ErrSessionCorrupt, loginRequired: true},
		{name: "store failure", store: &observedStore{err: errors.New("keyring unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewAuthenticatedClient(context.Background(), ClientOptions{Mode: auth.ModeAuto, Store: test.store})
			if err == nil {
				t.Fatal("NewAuthenticatedClient succeeded")
			}
			if got := errors.Is(err, ErrLoginRequired); got != test.loginRequired {
				t.Fatalf("errors.Is(ErrLoginRequired) = %v, want %v: %v", got, test.loginRequired, err)
			}
			if test.wantCause != nil && !errors.Is(err, test.wantCause) {
				t.Fatalf("error = %v, want cause %v", err, test.wantCause)
			}
			if !test.loginRequired && !strings.Contains(err.Error(), "keyring unavailable") {
				t.Fatalf("error = %v, want store failure", err)
			}
		})
	}
}

func TestNewAuthenticatedClientMarksExpiredUnrefreshableOAuthForLogin(t *testing.T) {
	clearStagingEnv(t)
	session := validSession()
	session.Expiry = time.Now().Add(-time.Hour)
	session.RefreshToken = ""
	store := &observedStore{session: session}

	_, err := NewAuthenticatedClient(context.Background(), ClientOptions{Mode: auth.ModeAuto, Store: store})
	if err == nil || !errors.Is(err, ErrLoginRequired) || !errors.Is(err, auth.ErrReauthRequired) {
		t.Fatalf("error = %v, want login-required reauthentication", err)
	}
}

func TestNewAuthenticatedClientRejectsInvalidConfiguration(t *testing.T) {
	for _, opts := range []ClientOptions{
		{Mode: auth.ModeAuto},
		{Mode: auth.Mode("future")},
	} {
		if _, err := NewAuthenticatedClient(context.Background(), opts); err == nil {
			t.Fatalf("NewAuthenticatedClient(%+v) succeeded", opts)
		}
	}
}

func TestNewEngineSelectionAndConfiguration(t *testing.T) {
	clearStagingEnv(t)
	t.Run("automatic fake bypasses OAuth", func(t *testing.T) {
		const id = "11111111-1111-4111-8111-111111111111"
		// A script instead of an id pushes the conversation it opens.
		for _, conversation := range []string{id, `say("earlier")`} {
			store := &observedStore{err: errors.New("must not load")}
			engine, err := NewEngine(context.Background(), EngineOptions{
				Client:         ClientOptions{Mode: auth.ModeAuto, Store: store},
				Send:           assistant.SendOptions{ConversationID: conversation},
				UseFakeBackend: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if store.loads != 0 {
				t.Fatalf("store loads = %d", store.loads)
			}
			got := engine.ConversationID()
			if (conversation == id) != (got == id) || !agent.ValidConversationID(got) {
				t.Fatalf("conversation ID = %q", got)
			}
		}
	})

	t.Run("API key takes precedence over fake", func(t *testing.T) {
		_, err := NewEngine(context.Background(), EngineOptions{
			Client:         ClientOptions{Mode: auth.ModeAPIKey, APISite: "api.datadoghq.com"},
			UseFakeBackend: true,
		})
		if err == nil || !strings.Contains(err.Error(), "DD_API_KEY and DD_APP_KEY") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unsupported mode cannot select fake", func(t *testing.T) {
		_, err := NewEngine(context.Background(), EngineOptions{
			Client:         ClientOptions{Mode: auth.Mode("future")},
			UseFakeBackend: true,
		})
		if err == nil || !strings.Contains(err.Error(), "unsupported authentication mode") {
			t.Fatalf("error = %v", err)
		}
	})
}

// API-key authentication for a configured staging environment targets the
// canonical API host even when the caller names the login origin; production
// login hosts stay rejected.
func TestNewAuthenticatedClientAPIKeyCanonicalizesStagingLoginOrigin(t *testing.T) {
	clearStagingEnv(t)
	t.Setenv(site.EnvStagingSite, "https://login.staging.test")
	t.Setenv(site.EnvStagingDomain, "staging.test")
	for _, apiSite := range []string{"https://login.staging.test", "https://api.staging.test"} {
		client, err := NewAuthenticatedClient(context.Background(), ClientOptions{
			Mode: auth.ModeAPIKey, APIKey: "api-key", AppKey: "app-key", APISite: apiSite,
		})
		if err != nil {
			t.Fatal(err)
		}
		if client.BaseURL != "https://api.staging.test" {
			t.Errorf("API-key client for %q targets %q, want the canonical API host", apiSite, client.BaseURL)
		}
	}
	if _, err := NewAuthenticatedClient(context.Background(), ClientOptions{
		Mode: auth.ModeAPIKey, APIKey: "api-key", AppKey: "app-key", APISite: "app.datadoghq.com",
	}); err == nil {
		t.Error("production login host accepted as an API-key site")
	}
}

// A stored staging session without staging configuration fails closed: the
// error is the actionable configuration failure itself, not a login-required
// or corrupt-session state, and no store mutation or network fallback
// happens. Callers therefore surface it instead of opening a login picker.
func TestNewAuthenticatedClientMissingStagingConfigFailsClosed(t *testing.T) {
	clearStagingEnv(t)
	session := validSession()
	session.Site = "https://api.staging.test"
	session.ClientID = "saved-staging-client"
	store := &observedStore{session: session}

	_, err := NewAuthenticatedClient(context.Background(), ClientOptions{Mode: auth.ModeAuto, Store: store})
	if err == nil {
		t.Fatal("NewAuthenticatedClient succeeded without staging configuration")
	}
	if !errors.Is(err, auth.ErrSiteConfiguration) {
		t.Fatalf("error = %v, want ErrSiteConfiguration", err)
	}
	if errors.Is(err, ErrLoginRequired) || errors.Is(err, auth.ErrSessionCorrupt) {
		t.Fatalf("error = %v, must not be login-required or corrupt-session so callers do not suppress it into a login picker", err)
	}
	for _, want := range []string{site.EnvStagingSite, site.EnvStagingDomain, "bits login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "logout") {
		t.Errorf("error advises deleting the stored credential: %v", err)
	}
	if store.loads != 1 || store.saves != 0 || store.deletes != 0 {
		t.Fatalf("OAuth store operations = load %d, save %d, delete %d", store.loads, store.saves, store.deletes)
	}
}

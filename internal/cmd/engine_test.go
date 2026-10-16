package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/agent"
	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/config"
)

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

// apiKeyConfig loads the configuration with the given API-key credentials.
func apiKeyConfig(t *testing.T, apiKey, appKey string) *config.Config {
	t.Helper()
	t.Setenv(config.EnvAPIKey, apiKey)
	t.Setenv(config.EnvAppKey, appKey)
	return loadConfig(t, config.Params{})
}

func TestNewClientAPIKeyBypassesOAuthStore(t *testing.T) {
	for _, store := range []*observedStore{
		{session: validSession()},
		{err: errors.New("keyring unavailable")},
	} {
		client, err := newClient(context.Background(), ChatOptions{
			Config:   apiKeyConfig(t, "api-key", "app-key"),
			AuthMode: auth.ModeAPIKey, Site: "api.datadoghq.eu",
		}, store)
		if err != nil {
			t.Fatal(err)
		}
		if store.loads != 0 || store.saves != 0 || store.deletes != 0 {
			t.Fatalf("OAuth store operations = load %d, save %d, delete %d", store.loads, store.saves, store.deletes)
		}
		if client.TokenSource != nil || client.BackendStatus().AuthenticationMode != "api-key" || client.BaseURL != "https://api.datadoghq.eu" ||
			client.WebHost != "app.datadoghq.eu" {
			t.Fatalf("API-key client = %#v", client)
		}
	}
}

func TestNewClientAPIKeyRequiresSecretsAndTrustedSite(t *testing.T) {
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
			_, err := newClient(context.Background(), ChatOptions{
				Config:   apiKeyConfig(t, test.apiKey, test.appKey),
				AuthMode: auth.ModeAPIKey, Site: test.site,
			}, store)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
			if store.loads != 0 || store.saves != 0 || store.deletes != 0 {
				t.Fatalf("OAuth store operations = load %d, save %d, delete %d", store.loads, store.saves, store.deletes)
			}
		})
	}
}

func TestNewClientAutoUsesOAuthAndIgnoresAmbientKeys(t *testing.T) {
	store := &observedStore{session: validSession()}
	client, err := newClient(context.Background(), ChatOptions{
		Config:   apiKeyConfig(t, "ignored-api", "ignored-app"),
		AuthMode: auth.ModeAuto, Site: "api.datadoghq.eu",
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	if client.TokenSource == nil || client.BackendStatus().AuthenticationMode != "oauth" {
		t.Fatalf("client auth = token source %v, mode %q", client.TokenSource != nil, client.BackendStatus().AuthenticationMode)
	}
	if client.BaseURL != "https://api.datadoghq.com" {
		t.Fatalf("BaseURL = %q", client.BaseURL)
	}
}

func TestNewClientClassifiesOnlyReplaceableOAuthFailures(t *testing.T) {
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
			_, err := newClient(context.Background(), ChatOptions{Config: &config.Config{}, AuthMode: auth.ModeAuto}, test.store)
			if err == nil {
				t.Fatal("newClient succeeded")
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

func TestNewClientMarksExpiredUnrefreshableOAuthForLogin(t *testing.T) {
	session := validSession()
	session.Expiry = time.Now().Add(-time.Hour)
	session.RefreshToken = ""
	store := &observedStore{session: session}

	_, err := newClient(context.Background(), ChatOptions{Config: &config.Config{}, AuthMode: auth.ModeAuto}, store)
	if err == nil || !errors.Is(err, ErrLoginRequired) || !errors.Is(err, auth.ErrReauthRequired) {
		t.Fatalf("error = %v, want login-required reauthentication", err)
	}
}

func TestNewClientRejectsInvalidConfiguration(t *testing.T) {
	for _, mode := range []auth.Mode{auth.ModeAuto, auth.Mode("future")} {
		if _, err := newClient(context.Background(), ChatOptions{Config: &config.Config{}, AuthMode: mode}, nil); err == nil {
			t.Fatalf("newClient(%q) succeeded", mode)
		}
	}
}

func TestNewEngineSelectionAndConfiguration(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	t.Run("automatic fake bypasses OAuth", func(t *testing.T) {
		const id = "11111111-1111-4111-8111-111111111111"
		// A script instead of an id pushes the conversation it opens.
		for _, conversation := range []string{id, `say("earlier")`} {
			store := &observedStore{err: errors.New("must not load")}
			engine, err := newEngine(context.Background(), ChatOptions{
				Config:   loadConfig(t, config.Params{}),
				AuthMode: auth.ModeAuto, ConversationID: conversation,
			}, nil, store)
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
		_, err := newEngine(context.Background(), ChatOptions{
			Config:   loadConfig(t, config.Params{}),
			AuthMode: auth.ModeAPIKey, Site: "api.datadoghq.com",
		}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "DD_API_KEY and DD_APP_KEY") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unsupported mode cannot select fake", func(t *testing.T) {
		_, err := newEngine(context.Background(), ChatOptions{Config: loadConfig(t, config.Params{}), AuthMode: auth.Mode("future")}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "unsupported authentication mode") {
			t.Fatalf("error = %v", err)
		}
	})
}

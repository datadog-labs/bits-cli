package startup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
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
		Site:         "https://api.datad0g.com",
		ClientID:     "oauth-client",
		AccessToken:  "oauth-access",
		RefreshToken: "oauth-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
}

func TestNewAuthenticatedClientAPIKeyBypassesOAuthStore(t *testing.T) {
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
	if client.BaseURL != "https://api.datad0g.com" {
		t.Fatalf("BaseURL = %q", client.BaseURL)
	}
}

func TestNewAuthenticatedClientClassifiesOnlyReplaceableOAuthFailures(t *testing.T) {
	for _, test := range []struct {
		name          string
		store         *observedStore
		wantCause     error
		loginRequired bool
	}{
		{name: "no session", store: &observedStore{err: auth.ErrNoSession}, wantCause: auth.ErrNoSession, loginRequired: true},
		{name: "corrupt session", store: &observedStore{err: errors.Join(auth.ErrSessionCorrupt, errors.New("truncated"))}, wantCause: auth.ErrSessionCorrupt, loginRequired: true},
		{name: "invalid decoded session", store: &observedStore{session: auth.Session{Site: "https://api.datad0g.com"}}, wantCause: auth.ErrSessionCorrupt, loginRequired: true},
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
	t.Run("automatic fake bypasses OAuth", func(t *testing.T) {
		store := &observedStore{err: errors.New("must not load")}
		engine, err := NewEngine(context.Background(), EngineOptions{
			Client:         ClientOptions{Mode: auth.ModeAuto, Store: store},
			Send:           assistant.SendOptions{ConversationID: "conversation-1"},
			UseFakeBackend: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if store.loads != 0 {
			t.Fatalf("store loads = %d", store.loads)
		}
		if got := engine.ConversationID(); got != "conversation-1" {
			t.Fatalf("conversation ID = %q", got)
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

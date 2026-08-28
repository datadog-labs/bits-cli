package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/assistant"
	"github.com/DataDog/bits-cli/internal/auth"
	"github.com/DataDog/bits-cli/internal/cmd"
)

type stubCredentialStore struct {
	session auth.Session
	err     error
}

func (s stubCredentialStore) Load() (auth.Session, error) { return s.session, s.err }
func (stubCredentialStore) Save(auth.Session) error       { return nil }
func (stubCredentialStore) Delete() error                 { return nil }

func authenticatedClientWith(store auth.CredentialStore, apiKey, appKey string) (*assistant.Client, error) {
	return authenticatedClientWithContext(context.Background(), store, apiKey, appKey, "", cmd.AuthenticationModeAuto)
}

func validOAuthSession() auth.Session {
	return auth.Session{
		Site:         "https://api.datad0g.com",
		ClientID:     "oauth-client",
		AccessToken:  "oauth-access",
		RefreshToken: "oauth-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
}

type observedCredentialStore struct {
	loads   int
	saves   int
	deletes int
	session auth.Session
	err     error
}

func (s *observedCredentialStore) Load() (auth.Session, error) {
	s.loads++
	return s.session, s.err
}

func (s *observedCredentialStore) Save(auth.Session) error {
	s.saves++
	return nil
}

func (s *observedCredentialStore) Delete() error {
	s.deletes++
	return nil
}

func TestExplicitAPIKeyModeBypassesOAuthStore(t *testing.T) {
	for _, test := range []struct {
		name  string
		store *observedCredentialStore
	}{
		{name: "stored OAuth session", store: &observedCredentialStore{session: validOAuthSession()}},
		{name: "credential store failure", store: &observedCredentialStore{err: errors.New("keyring unavailable")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := authenticatedClientWithContext(
				context.Background(),
				test.store,
				"api-key",
				"app-key",
				"api.datadoghq.eu",
				cmd.AuthenticationModeAPIKey,
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.store.loads != 0 || test.store.saves != 0 || test.store.deletes != 0 {
				t.Fatalf("OAuth store operations = load %d, save %d, delete %d", test.store.loads, test.store.saves, test.store.deletes)
			}
			if client.TokenSource != nil || client.APIKey != "api-key" || client.AppKey != "app-key" || client.BaseURL != "https://api.datadoghq.eu" {
				t.Fatalf("API-key client = %#v", client)
			}
		})
	}
}

func TestExplicitAPIKeyModeRequiresBothSecretsWithoutReadingOAuthStore(t *testing.T) {
	for _, keys := range [][2]string{{}, {"api-only", ""}, {"", "app-only"}} {
		store := &observedCredentialStore{session: validOAuthSession()}
		_, err := authenticatedClientWithContext(
			context.Background(), store, keys[0], keys[1], "api.datadoghq.com", cmd.AuthenticationModeAPIKey,
		)
		if err == nil || !strings.Contains(err.Error(), "DD_API_KEY and DD_APP_KEY must both be set") {
			t.Fatalf("keys %q/%q error = %v", keys[0], keys[1], err)
		}
		if store.loads != 0 || store.saves != 0 || store.deletes != 0 {
			t.Fatalf("keys %q/%q OAuth store operations = load %d, save %d, delete %d", keys[0], keys[1], store.loads, store.saves, store.deletes)
		}
	}
}

func TestExplicitAPIKeyModeRejectsMissingOrUntrustedSitesWithoutReadingOAuthStore(t *testing.T) {
	for _, site := range []string{
		"",
		"https://example.com",
		"http://api.datadoghq.com",
		"https://api.datadoghq.com/unexpected",
		"https://app.datadoghq.com",
		"https://datadoghq.com.example.com",
	} {
		store := &observedCredentialStore{session: validOAuthSession()}
		_, err := authenticatedClientWithContext(
			context.Background(), store, "api-key", "app-key", site, cmd.AuthenticationModeAPIKey,
		)
		if err == nil {
			t.Fatalf("site %q unexpectedly accepted", site)
		}
		if store.loads != 0 || store.saves != 0 || store.deletes != 0 {
			t.Fatalf("site %q OAuth store operations = load %d, save %d, delete %d", site, store.loads, store.saves, store.deletes)
		}
	}
}

func TestStartupExplicitAPIKeyModeDoesNotFallThroughToFakeBackend(t *testing.T) {
	t.Setenv("BITS_FAKE_BACKEND", "1")
	t.Setenv("DD_API_KEY", "")
	t.Setenv("DD_APP_KEY", "")
	_, err := startupModel(context.Background(), cmd.ChatOptions{AuthMode: cmd.AuthenticationModeAPIKey, Site: "api.datadoghq.com"})
	if err == nil || !strings.Contains(err.Error(), "DD_API_KEY and DD_APP_KEY must both be set") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestAuthenticatedClientPrefersStoredOAuthOverCompleteKeys(t *testing.T) {
	client, err := authenticatedClientWith(
		stubCredentialStore{session: validOAuthSession()},
		"api-key", "app-key",
	)
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

func TestAutomaticModeDoesNotUseAmbientAPIKeys(t *testing.T) {
	for _, keys := range [][2]string{{}, {"api-only", ""}, {"", "app-only"}, {"api-key", "app-key"}} {
		_, err := authenticatedClientWith(stubCredentialStore{err: auth.ErrNoSession}, keys[0], keys[1])
		if !errors.Is(err, errNoWorkingAuth) {
			t.Fatalf("keys %q/%q error = %v, want no working OAuth", keys[0], keys[1], err)
		}
	}
}

func TestAuthenticatedClientDoesNotHideCredentialStoreErrors(t *testing.T) {
	keyringErr := errors.New("keyring unavailable")
	_, err := authenticatedClientWith(
		stubCredentialStore{err: keyringErr},
		"api-key", "app-key",
	)
	if !errors.Is(err, keyringErr) {
		t.Fatalf("error = %v, want keyring error", err)
	}
}

func TestAuthenticatedClientExplainsCorruptStoredSession(t *testing.T) {
	_, err := authenticatedClientWith(
		stubCredentialStore{err: fmt.Errorf("%w: truncated", auth.ErrSessionCorrupt)},
		"api-key", "app-key",
	)
	if err == nil || !errors.Is(err, auth.ErrSessionCorrupt) || !strings.Contains(err.Error(), "run `bits logout`, then `bits login`") {
		t.Fatalf("error = %v", err)
	}
}

func TestAuthenticatedClientDoesNotFallbackFromInvalidStoredSession(t *testing.T) {
	_, err := authenticatedClientWith(
		stubCredentialStore{session: auth.Session{Site: "https://api.datad0g.com"}},
		"api-key", "app-key",
	)
	if err == nil {
		t.Fatal("invalid OAuth session silently fell back to API keys")
	}
}

func TestCanStartLoginOnlyForReplaceableAuthenticationFailures(t *testing.T) {
	for _, err := range []error{
		errNoWorkingAuth,
		fmt.Errorf("wrapped: %w", auth.ErrSessionCorrupt),
		auth.ErrReauthRequired,
	} {
		if !canStartLogin(err) {
			t.Errorf("canStartLogin(%v) = false", err)
		}
	}
	for _, err := range []error{nil, errors.New("keyring unavailable"), context.Canceled} {
		if canStartLogin(err) {
			t.Errorf("canStartLogin(%v) = true", err)
		}
	}
}

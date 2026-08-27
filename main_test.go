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
)

type stubCredentialStore struct {
	session auth.Session
	err     error
}

func (s stubCredentialStore) Load() (auth.Session, error) { return s.session, s.err }
func (stubCredentialStore) Save(auth.Session) error       { return nil }
func (stubCredentialStore) Delete() error                 { return nil }

type mutableCredentialStore struct {
	session auth.Session
	err     error
}

func (s *mutableCredentialStore) Load() (auth.Session, error) { return s.session, s.err }
func (s *mutableCredentialStore) Save(session auth.Session) error {
	s.session, s.err = session, nil
	return nil
}

func (s *mutableCredentialStore) Delete() error {
	s.session, s.err = auth.Session{}, auth.ErrNoSession
	return nil
}

func authenticatedClientWith(store auth.CredentialStore, apiKey, appKey, apiSite string) (*assistant.Client, error) {
	return authenticatedClientWithContext(context.Background(), store, apiKey, appKey, apiSite)
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

func TestHelpReturnsSuccess(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "chat", args: []string{"-h"}},
		{name: "login", args: []string{"login", "-h"}},
		{name: "logout", args: []string{"logout", "-h"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := run(test.args); err != nil {
				t.Fatalf("run(%q) = %v, want successful help", test.args, err)
			}
		})
	}
}

func TestDefaultLoginSite(t *testing.T) {
	if got := defaultLoginSite(""); got != auth.DefaultSite {
		t.Fatalf("defaultLoginSite(\"\") = %q, want %q", got, auth.DefaultSite)
	}
	if got := defaultLoginSite(auth.DefaultStagingSite); got != auth.DefaultStagingSite {
		t.Fatalf("defaultLoginSite(staging) = %q, want configured site", got)
	}
}

func TestAuthenticatedClientPrefersStoredOAuthOverCompleteKeys(t *testing.T) {
	client, err := authenticatedClientWith(
		stubCredentialStore{session: validOAuthSession()},
		"api-key", "app-key", "https://keys.example.com",
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

func TestAuthenticatedClientFallsBackOnlyForNoSessionAndCompleteKeys(t *testing.T) {
	client, err := authenticatedClientWith(
		stubCredentialStore{err: auth.ErrNoSession},
		"api-key", "app-key", "https://keys.example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	if client.TokenSource != nil || client.APIKey != "api-key" || client.AppKey != "app-key" || client.BaseURL != "https://keys.example.com" {
		t.Fatalf("fallback client = %#v", client)
	}

	for _, keys := range [][2]string{{}, {"api-only", ""}, {"", "app-only"}} {
		if _, err := authenticatedClientWith(stubCredentialStore{err: auth.ErrNoSession}, keys[0], keys[1], ""); err == nil {
			t.Fatalf("keys %q/%q unexpectedly authenticated", keys[0], keys[1])
		}
	}
}

func TestAuthenticatedClientDoesNotHideCredentialStoreErrors(t *testing.T) {
	keyringErr := errors.New("keyring unavailable")
	_, err := authenticatedClientWith(
		stubCredentialStore{err: keyringErr},
		"api-key", "app-key", "https://keys.example.com",
	)
	if !errors.Is(err, keyringErr) {
		t.Fatalf("error = %v, want keyring error", err)
	}
}

func TestAuthenticatedClientExplainsCorruptStoredSession(t *testing.T) {
	_, err := authenticatedClientWith(
		stubCredentialStore{err: fmt.Errorf("%w: truncated", auth.ErrSessionCorrupt)},
		"api-key", "app-key", "https://keys.example.com",
	)
	if err == nil || !errors.Is(err, auth.ErrSessionCorrupt) || !strings.Contains(err.Error(), "run `bits logout`, then `bits login`") {
		t.Fatalf("error = %v", err)
	}
}

func TestAuthenticatedClientDoesNotFallbackFromInvalidStoredSession(t *testing.T) {
	_, err := authenticatedClientWith(
		stubCredentialStore{session: auth.Session{Site: "https://api.datad0g.com"}},
		"api-key", "app-key", "https://keys.example.com",
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

func TestEnsureAuthenticatedClientRunsLoginOnlyWhenNeeded(t *testing.T) {
	store := &mutableCredentialStore{err: auth.ErrNoSession}
	loginCalls := 0
	client, err := ensureAuthenticatedClient(context.Background(), store, "", "", "", func() error {
		loginCalls++
		return store.Save(validOAuthSession())
	})
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls != 1 || client.TokenSource == nil {
		t.Fatalf("login calls = %d, OAuth client = %t", loginCalls, client.TokenSource != nil)
	}

	loginCalls = 0
	if _, err := ensureAuthenticatedClient(context.Background(), store, "", "", "", func() error {
		loginCalls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if loginCalls != 0 {
		t.Fatalf("login called %d times with working OAuth", loginCalls)
	}
}

func TestEnsureAuthenticatedClientKeepsCompleteAPIKeys(t *testing.T) {
	store := &mutableCredentialStore{err: auth.ErrNoSession}
	loginCalls := 0
	client, err := ensureAuthenticatedClient(context.Background(), store, "api", "app", "https://keys.example.com", func() error {
		loginCalls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls != 0 || client.APIKey != "api" || client.AppKey != "app" {
		t.Fatalf("login calls = %d, client = %#v", loginCalls, client)
	}
}

func TestEnsureAuthenticatedClientRepairsCorruptSession(t *testing.T) {
	store := &mutableCredentialStore{err: fmt.Errorf("%w: truncated", auth.ErrSessionCorrupt)}
	loginCalls := 0
	_, err := ensureAuthenticatedClient(context.Background(), store, "api", "app", "", func() error {
		loginCalls++
		return store.Save(validOAuthSession())
	})
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1", loginCalls)
	}
}

func TestEnsureAuthenticatedClientRepairsDecodedSessionWithInvalidRouting(t *testing.T) {
	store := &mutableCredentialStore{session: auth.Session{
		Site:         "https://example.com",
		ClientID:     "oauth-client",
		AccessToken:  "oauth-access",
		RefreshToken: "oauth-refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}}
	loginCalls := 0
	_, err := ensureAuthenticatedClient(context.Background(), store, "", "", "", func() error {
		loginCalls++
		return store.Save(validOAuthSession())
	})
	if err != nil {
		t.Fatal(err)
	}
	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1", loginCalls)
	}
}

func TestEnsureAuthenticatedClientSurfacesStoreAndLoginErrors(t *testing.T) {
	storeErr := errors.New("keyring unavailable")
	loginCalls := 0
	_, err := ensureAuthenticatedClient(context.Background(), stubCredentialStore{err: storeErr}, "", "", "", func() error {
		loginCalls++
		return nil
	})
	if !errors.Is(err, storeErr) || loginCalls != 0 {
		t.Fatalf("store error = %v, login calls = %d", err, loginCalls)
	}

	loginErr := errors.New("authorization denied")
	_, err = ensureAuthenticatedClient(context.Background(), stubCredentialStore{err: auth.ErrNoSession}, "", "", "", func() error {
		return loginErr
	})
	if !errors.Is(err, loginErr) {
		t.Fatalf("login error = %v", err)
	}
}

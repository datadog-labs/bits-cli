package main

import (
	"errors"
	"testing"
	"time"

	"github.com/DataDog/bits-cli/internal/auth"
)

type stubCredentialStore struct {
	session auth.Session
	err     error
}

func (s stubCredentialStore) Load() (auth.Session, error) { return s.session, s.err }
func (stubCredentialStore) Save(auth.Session) error       { return nil }
func (stubCredentialStore) Delete() error                 { return nil }

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

func TestAuthenticatedClientDoesNotFallbackFromInvalidStoredSession(t *testing.T) {
	_, err := authenticatedClientWith(
		stubCredentialStore{session: auth.Session{Site: "https://api.datad0g.com"}},
		"api-key", "app-key", "https://keys.example.com",
	)
	if err == nil {
		t.Fatal("invalid OAuth session silently fell back to API keys")
	}
}

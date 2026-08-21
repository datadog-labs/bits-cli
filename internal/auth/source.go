package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const refreshWindow = 5 * time.Minute

// Source supplies access tokens, refreshing and persisting them as needed. Its
// mutex is load-bearing for refresh-token rotation: concurrent Assistant calls
// must not try to consume the same refresh token twice.
type Source struct {
	mu         sync.Mutex
	config     SiteConfig
	store      CredentialStore
	httpClient *http.Client
	session    Session
	dirty      bool
}

// NewSource builds a refreshing source from a stored session.
func NewSource(session Session, store CredentialStore, httpClient *http.Client) (*Source, error) {
	if err := session.validate(); err != nil {
		return nil, err
	}
	cfg, err := ConfigForSite(session.Site, session.ClientID)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Source{config: cfg, store: store, httpClient: httpClient, session: session}, nil
}

// Site is the Assistant base URL associated with this login.
func (s *Source) Site() string {
	return s.config.AssistantBase
}

// AccessToken implements assistant.AccessTokenSource.
func (s *Source) AccessToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.dirty {
		if err := s.store.Save(s.session); err != nil {
			return "", fmt.Errorf("persist rotated OAuth token: %w", err)
		}
		s.dirty = false
	}

	current := s.session.token()
	if !needsRefresh(current) {
		return current.AccessToken, nil
	}
	if current.RefreshToken == "" {
		return "", fmt.Errorf("OAuth access token expired and no refresh token is available; run bits login again")
	}

	// Config.TokenSource normally waits until expiry. Shift only the disposable
	// copy into the past so refresh starts five minutes early while preserving
	// the real expiry stored in s.session.
	candidate := *current
	candidate.Expiry = time.Now().Add(-time.Minute)
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, s.httpClient)
	refreshed, err := s.config.OAuth2Config().TokenSource(ctx, &candidate).Token()
	if err != nil {
		return "", fmt.Errorf("refresh Datadog OAuth token: %w", err)
	}

	s.session = sessionFromToken(s.config, refreshed)
	s.dirty = true
	if err := s.store.Save(s.session); err != nil {
		return "", fmt.Errorf("persist rotated OAuth token: %w", err)
	}
	s.dirty = false
	return refreshed.AccessToken, nil
}

func needsRefresh(token *oauth2.Token) bool {
	if token.AccessToken == "" {
		return true
	}
	if token.Expiry.IsZero() {
		return false
	}
	return time.Until(token.Expiry) <= refreshWindow
}

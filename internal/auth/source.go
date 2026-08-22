package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const (
	refreshWindow  = 5 * time.Minute
	refreshTimeout = 30 * time.Second
)

// Source supplies access tokens, refreshing and persisting them as needed.
// Every durable transition also takes the store's cross-process lock so two
// Bits processes cannot consume or overwrite the same rotating token lineage.
type Source struct {
	mu          sync.Mutex
	config      SiteConfig
	store       CredentialStore
	httpClient  *http.Client
	session     Session
	dirty       bool
	persistBase Session
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
	if store == nil {
		return nil, fmt.Errorf("OAuth credential store is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: refreshTimeout}
	}
	return &Source{config: cfg, store: store, httpClient: httpClient, session: session}, nil
}

// Site is the Assistant base URL associated with this login.
func (s *Source) Site() string {
	return s.config.AssistantBase
}

// AccessToken implements assistant.AccessTokenSource.
func (s *Source) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var accessToken string
	err := withSessionLock(ctx, s.store, func() error {
		durable, loadErr := s.store.Load()
		if s.dirty {
			if err := s.reconcileDirty(ctx, durable, loadErr); err != nil {
				return err
			}
		} else {
			if loadErr != nil {
				if errors.Is(loadErr, ErrNoSession) {
					return fmt.Errorf("%w: the stored session was removed; run `bits login`", ErrReauthRequired)
				}
				return loadErr
			}
			if !sameSession(durable, s.session) {
				if err := s.adopt(durable); err != nil {
					return err
				}
			}
		}

		current := s.session.token()
		if !needsRefresh(current) {
			accessToken = current.AccessToken
			return nil
		}
		if current.RefreshToken == "" {
			return s.invalidateLocked(ctx, fmt.Errorf("the access token expired without a refresh token"))
		}

		// Config.TokenSource normally waits until expiry. Shift only the
		// disposable copy into the past so refresh starts five minutes early.
		candidate := *current
		candidate.Expiry = time.Now().Add(-time.Minute)
		refreshCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
		defer cancel()
		refreshCtx = context.WithValue(refreshCtx, oauth2.HTTPClient, s.httpClient)
		refreshed, err := s.config.OAuth2Config().TokenSource(refreshCtx, &candidate).Token()
		if err != nil {
			if isInvalidGrant(err) {
				return s.invalidateLocked(ctx, fmt.Errorf("the authorization server rejected the refresh grant"))
			}
			return sanitizedRefreshError(err)
		}

		before := s.session
		s.session = sessionFromToken(s.config, refreshed)
		s.dirty = true
		s.persistBase = before
		if err := saveWithRetry(ctx, s.store, s.session); err != nil {
			// Keep the rotated token in memory. A later call may secure it only if
			// the durable predecessor is unchanged; it may never overwrite a
			// replacement login or resurrect a deleted session.
			return fmt.Errorf("%w: %w", ErrSessionNotDurable, err)
		}
		s.dirty = false
		s.persistBase = Session{}
		accessToken = refreshed.AccessToken
		return nil
	})
	if err != nil {
		return "", err
	}
	return accessToken, nil
}

func (s *Source) reconcileDirty(ctx context.Context, durable Session, loadErr error) error {
	if loadErr != nil {
		if errors.Is(loadErr, ErrNoSession) {
			return fmt.Errorf("%w: the stored session was removed while a rotated token was pending; run `bits login`", ErrReauthRequired)
		}
		return fmt.Errorf("%w: %w", ErrSessionNotDurable, loadErr)
	}

	switch {
	case sameSession(durable, s.session):
		s.dirty = false
		s.persistBase = Session{}
		return nil
	case sameSession(durable, s.persistBase):
		if err := saveWithRetry(ctx, s.store, s.session); err != nil {
			return fmt.Errorf("%w: %w", ErrSessionNotDurable, err)
		}
		s.dirty = false
		s.persistBase = Session{}
		return nil
	default:
		// Another process committed a newer login or rotation. Never overwrite
		// it with this process's pending state.
		s.dirty = false
		s.persistBase = Session{}
		return s.adopt(durable)
	}
}

func (s *Source) adopt(session Session) error {
	if err := session.validate(); err != nil {
		return err
	}
	if session.Site != s.session.Site || session.ClientID != s.session.ClientID {
		return fmt.Errorf("%w; restart Bits to use the new login", ErrSessionReplaced)
	}
	s.session = session
	return nil
}

func (s *Source) invalidateLocked(ctx context.Context, reason error) error {
	deleteErr := deleteWithRetry(ctx, s.store)
	s.session = Session{}
	s.dirty = false
	s.persistBase = Session{}
	if deleteErr != nil {
		return fmt.Errorf("%w: %w; also failed to remove the unusable stored session: %w", ErrReauthRequired, reason, deleteErr)
	}
	return fmt.Errorf("%w: %w; run `bits login`", ErrReauthRequired, reason)
}

func isInvalidGrant(err error) bool {
	var retrieveErr *oauth2.RetrieveError
	return errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant"
}

func sanitizedRefreshError(err error) error {
	var retrieveErr *oauth2.RetrieveError
	if !errors.As(err, &retrieveErr) {
		return fmt.Errorf("refresh Datadog OAuth token: %w", err)
	}
	status := 0
	if retrieveErr.Response != nil {
		status = retrieveErr.Response.StatusCode
	}
	if code := safeOAuthErrorCode(retrieveErr.ErrorCode); code != "" {
		return fmt.Errorf("refresh Datadog OAuth token: HTTP %d (%s)", status, code)
	}
	return fmt.Errorf("refresh Datadog OAuth token: HTTP %d", status)
}

func needsRefresh(token *oauth2.Token) bool {
	if token.AccessToken == "" {
		return true
	}
	if token.Expiry.IsZero() {
		// Datadog-issued sessions normally carry an expiry. Treat a missing one
		// conservatively instead of using an access token indefinitely.
		return true
	}
	return time.Until(token.Expiry) <= refreshWindow
}

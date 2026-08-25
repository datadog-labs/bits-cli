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
	gateOnce     sync.Once
	gate         chan struct{}
	config       SiteConfig
	store        CredentialStore
	httpClient   *http.Client
	session      Session
	dirty        bool
	persistBase  Session
	cleanupGrant Session
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
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()

	var accessToken string
	lockCtx, lockCancel := context.WithTimeout(ctx, sessionLockTimeout)
	defer lockCancel()
	err := withSessionLock(lockCtx, s.store, func() error {
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
		// Once a rotating-token request begins, complete it even if the caller
		// cancels. Abandoning an in-flight response can consume the durable
		// refresh token without giving us the replacement to persist.
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
		defer cancel()
		refreshCtx = context.WithValue(refreshCtx, oauth2.HTTPClient, s.httpClient)
		refreshed, err := s.config.OAuth2Config().TokenSource(refreshCtx, &candidate).Token()
		if err != nil {
			if isInvalidGrant(err) {
				return s.invalidateLocked(ctx, fmt.Errorf("the authorization server rejected the refresh grant"))
			}
			return sanitizedOAuthError("refresh Datadog OAuth token", err)
		}
		before := s.session
		s.session = sessionFromToken(s.config, refreshed)
		s.dirty = true
		s.persistBase = before
		persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), sessionPersistTimeout)
		defer persistCancel()
		if err := saveWithRetry(persistCtx, s.store, s.session); err != nil {
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
	cleanup := s.cleanupGrant
	s.cleanupGrant = Session{}
	if cleanup.AccessToken != "" {
		s.revokeGrant(cleanup)
	}
	if err != nil {
		return "", err
	}
	return accessToken, nil
}

func (s *Source) reconcileDirty(ctx context.Context, durable Session, loadErr error) error {
	if loadErr != nil {
		if errors.Is(loadErr, ErrSessionCorrupt) {
			return loadErr
		}
		if errors.Is(loadErr, ErrNoSession) {
			s.queueDirtyGrantCleanup()
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
		// it and never revoke here: rotated tokens may share a grant family with
		// the durable replacement, so revoking the stale copy could kill the
		// active session.
		s.dirty = false
		s.persistBase = Session{}
		return s.adopt(durable)
	}
}

func (s *Source) adopt(session Session) error {
	if err := session.validate(); err != nil {
		return err
	}
	if session.Site != s.config.Site || session.ClientID != s.config.ClientID {
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

func (s *Source) acquire(ctx context.Context) error {
	s.gateOnce.Do(func() { s.gate = make(chan struct{}, 1) })
	select {
	case s.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Source) release() {
	<-s.gate
}

func (s *Source) queueDirtyGrantCleanup() {
	s.cleanupGrant = s.session
	s.session = Session{}
	s.dirty = false
	s.persistBase = Session{}
}

func (s *Source) revokeGrant(session Session) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = Revoke(cleanupCtx, session, s.httpClient)
}

// RejectAccessToken marks only the access-token generation rejected by a 401
// as stale. The failed request is never replayed; the next independently
// initiated request refreshes under the normal durable transaction.
func (s *Source) RejectAccessToken(ctx context.Context, rejected string) error {
	if rejected == "" {
		return nil
	}
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()
	return withSessionLock(ctx, s.store, func() error {
		durable, err := s.store.Load()
		if errors.Is(err, ErrNoSession) {
			return nil
		}
		if err != nil {
			return err
		}
		if durable.AccessToken != rejected {
			return nil
		}
		stale := durable
		stale.Expiry = time.Now().Add(-time.Minute)
		if err := saveWithRetry(ctx, s.store, stale); err != nil {
			s.session = stale
			s.dirty = true
			s.persistBase = durable
			return fmt.Errorf("%w: %w", ErrSessionNotDurable, err)
		}
		return s.adopt(stale)
	})
}

func isInvalidGrant(err error) bool {
	var retrieveErr *oauth2.RetrieveError
	return errors.As(err, &retrieveErr) && retrieveErr.ErrorCode == "invalid_grant"
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

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const (
	keyringService = "com.datadog.bits-cli"
	keyringAccount = "oauth-session"

	persistenceAttempts = 3
	persistenceRetry    = 75 * time.Millisecond
	lockRetry           = 50 * time.Millisecond
)

var (
	// ErrNoSession means no OAuth login is present in the OS credential store.
	ErrNoSession = errors.New("no Bits CLI OAuth session")
	// ErrReauthRequired means the stored grant cannot be refreshed safely and a
	// new interactive login is required.
	ErrReauthRequired = errors.New("OAuth login is no longer valid")
	// ErrSessionNotDurable means a newly rotated token is valid in this process
	// but could not be secured in the OS credential store.
	ErrSessionNotDurable = errors.New("OAuth session could not be persisted")
	// ErrSessionReplaced means another process replaced the active login with a
	// session whose routing cannot be adopted by the current Assistant client.
	ErrSessionReplaced = errors.New("OAuth session changed")
)

// Session is the durable subset of an OAuth token plus the routing information
// needed to refresh it and call the Assistant API.
type Session struct {
	Site         string    `json:"site"`
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

func sessionFromToken(cfg SiteConfig, token *oauth2.Token) Session {
	return Session{
		Site:         cfg.Site,
		ClientID:     cfg.ClientID,
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
		Expiry:       token.Expiry,
	}
}

func (s Session) token() *oauth2.Token {
	return &oauth2.Token{
		AccessToken:  s.AccessToken,
		TokenType:    s.TokenType,
		RefreshToken: s.RefreshToken,
		Expiry:       s.Expiry,
	}
}

func (s Session) validate() error {
	if s.Site == "" || s.ClientID == "" || s.AccessToken == "" {
		return fmt.Errorf("stored OAuth session is incomplete")
	}
	return nil
}

func sameSession(a, b Session) bool {
	return a.Site == b.Site &&
		a.ClientID == b.ClientID &&
		a.AccessToken == b.AccessToken &&
		a.TokenType == b.TokenType &&
		a.RefreshToken == b.RefreshToken &&
		a.Expiry.Equal(b.Expiry)
}

// CredentialStore persists OAuth sessions. Implementations must protect both
// access and rotating refresh tokens as secrets.
type CredentialStore interface {
	Load() (Session, error)
	Save(Session) error
	Delete() error
}

type sessionLocker interface {
	WithSessionLock(context.Context, func() error) error
}

var fallbackSessionLock sync.Mutex

// withSessionLock serializes every durable session transition. KeyringStore's
// implementation is cross-process; the fallback exists for test/custom stores
// and only serializes callers in this process.
func withSessionLock(ctx context.Context, store CredentialStore, fn func() error) error {
	if locker, ok := store.(sessionLocker); ok {
		return locker.WithSessionLock(ctx, fn)
	}
	fallbackSessionLock.Lock()
	defer fallbackSessionLock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func saveWithRetry(ctx context.Context, store CredentialStore, session Session) error {
	var firstErr error
	for attempt := range persistenceAttempts {
		if err := store.Save(session); err == nil {
			return nil
		} else if firstErr == nil {
			firstErr = err
		}
		if attempt == persistenceAttempts-1 {
			break
		}
		timer := time.NewTimer(persistenceRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(firstErr, ctx.Err())
		case <-timer.C:
		}
	}
	return firstErr
}

func deleteWithRetry(ctx context.Context, store CredentialStore) error {
	var firstErr error
	for attempt := range persistenceAttempts {
		if err := store.Delete(); err == nil {
			return nil
		} else if firstErr == nil {
			firstErr = err
		}
		if attempt == persistenceAttempts-1 {
			break
		}
		timer := time.NewTimer(persistenceRetry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(firstErr, ctx.Err())
		case <-timer.C:
		}
	}
	return firstErr
}

// KeyringStore stores the single active Bits CLI session in the native OS
// credential manager (Keychain, Secret Service, or Windows Credential Manager).
type KeyringStore struct {
	// LockPath is only overridden by tests. The default is in the user's config
	// directory and contains no credential material.
	LockPath string
}

func (KeyringStore) Load() (Session, error) {
	raw, err := keyring.Get(keyringService, keyringAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("read OAuth session from OS credential store: %w", err)
	}
	var session Session
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return Session{}, fmt.Errorf("decode OAuth session from OS credential store: %w", err)
	}
	if err := session.validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (KeyringStore) Save(session Session) error {
	if err := session.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode OAuth session: %w", err)
	}
	if err := keyring.Set(keyringService, keyringAccount, string(raw)); err != nil {
		return fmt.Errorf("write OAuth session to OS credential store: %w", err)
	}
	return nil
}

func (KeyringStore) Delete() error {
	err := keyring.Delete(keyringService, keyringAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete OAuth session from OS credential store: %w", err)
	}
	return nil
}

func (s KeyringStore) WithSessionLock(ctx context.Context, fn func() error) (err error) {
	path := s.LockPath
	if path == "" {
		configDir, configErr := os.UserConfigDir()
		if configErr != nil {
			return fmt.Errorf("locate OAuth session lock directory: %w", configErr)
		}
		path = filepath.Join(configDir, "bits-cli", "oauth-session.lock")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create OAuth session lock directory: %w", err)
	}

	fileLock := flock.New(path)
	locked, err := fileLock.TryLockContext(ctx, lockRetry)
	if err != nil {
		return fmt.Errorf("lock OAuth session: %w", err)
	}
	if !locked {
		return fmt.Errorf("lock OAuth session: %w", context.Cause(ctx))
	}
	defer func() {
		if unlockErr := fileLock.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock OAuth session: %w", unlockErr))
		}
	}()
	return fn()
}

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
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
	// ErrSessionCorrupt means a credential exists but cannot be decoded or
	// trusted. Login may replace it and logout may delete it, but chat must not
	// silently fall back to a different principal.
	ErrSessionCorrupt = errors.New("stored OAuth session is unreadable")
	// ErrReauthRequired means the stored grant cannot be refreshed safely and a
	// new interactive login is required.
	ErrReauthRequired = errors.New("OAuth login is no longer valid")
	// ErrSessionNotDurable means a newly rotated token is valid in this process
	// but could not be secured in the OS credential store.
	ErrSessionNotDurable = errors.New("OAuth session could not be persisted")
	// ErrSessionReplaced means another process replaced the active login with a
	// session whose routing cannot be adopted by the current Assistant client.
	ErrSessionReplaced = errors.New("OAuth session changed")
	// ErrSessionUnlock means the durable transaction completed but releasing the
	// cross-process lock reported an error. Callers must not roll back or revoke
	// an already-committed mutation in response.
	ErrSessionUnlock = errors.New("OAuth session lock release failed")
	// ErrSessionMutationUnknown means credential-manager IPC failed and the
	// durable postcondition could not be read back. Destructive cleanup must not
	// assume whether the mutation committed.
	ErrSessionMutationUnknown = errors.New("OAuth session mutation outcome is unknown")
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

func sessionFromToken(cfg SiteConfig, token *oauth2.Token, fallbackRefreshToken string) Session {
	expiry := token.Expiry
	if expiry.IsZero() {
		// expires_in is optional in OAuth. A bounded default avoids treating the
		// token as valid forever or rotating it again on every request.
		expiry = time.Now().Add(2 * refreshWindow)
	}
	refreshToken := token.RefreshToken
	if refreshToken == "" {
		refreshToken = fallbackRefreshToken
	}
	return Session{
		Site:         cfg.Site,
		ClientID:     cfg.ClientID,
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: refreshToken,
		Expiry:       expiry,
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
	if s.TokenType != "" && !strings.EqualFold(s.TokenType, "Bearer") {
		return fmt.Errorf("stored OAuth token type is unsupported")
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

var fallbackSessionGate = make(chan struct{}, 1)

// withSessionLock serializes every durable session transition. KeyringStore's
// implementation is cross-process; the fallback exists for test/custom stores
// and only serializes callers in this process.
func withSessionLock(ctx context.Context, store CredentialStore, fn func() error) error {
	if locker, ok := store.(sessionLocker); ok {
		return locker.WithSessionLock(ctx, fn)
	}
	select {
	case fallbackSessionGate <- struct{}{}:
		defer func() { <-fallbackSessionGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return fn()
}

func saveWithRetry(ctx context.Context, store CredentialStore, session Session) error {
	var firstErr error
	for attempt := range persistenceAttempts {
		if err := store.Save(session); err == nil {
			return nil
		} else {
			if firstErr == nil {
				firstErr = err
			}
			// Credential-manager IPC can commit and then lose its response.
			// Read back while still locked before deciding the save failed.
			if stored, loadErr := store.Load(); loadErr == nil && sameSession(stored, session) {
				return nil
			}
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
		} else {
			if firstErr == nil {
				firstErr = err
			}
			// As with Save, deletion may have committed before IPC failed.
			if _, loadErr := store.Load(); errors.Is(loadErr, ErrNoSession) {
				return nil
			}
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
		return Session{}, fmt.Errorf("%w: decode credential: %w", ErrSessionCorrupt, err)
	}
	if err := session.validate(); err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrSessionCorrupt, err)
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

func defaultSessionLockPath() (string, error) {
	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("locate OAuth session lock owner: %w", err)
	}
	if currentUser.HomeDir == "" {
		return "", fmt.Errorf("locate OAuth session lock owner: home directory is empty")
	}
	// The keyring account is shared per OS user, so the lock path must not vary
	// with HOME/XDG_CONFIG_HOME or another process environment.
	return filepath.Join(currentUser.HomeDir, ".bits-cli", "oauth-session.lock"), nil
}

func (s KeyringStore) WithSessionLock(ctx context.Context, fn func() error) (err error) {
	path := s.LockPath
	usingDefaultPath := path == ""
	if usingDefaultPath {
		var pathErr error
		path, pathErr = defaultSessionLockPath()
		if pathErr != nil {
			return pathErr
		}
	}
	lockDir := filepath.Dir(path)
	if usingDefaultPath {
		if info, statErr := os.Lstat(lockDir); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("create OAuth session lock directory: refusing symlink %s", lockDir)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect OAuth session lock directory: %w", statErr)
		}
	}
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return fmt.Errorf("create OAuth session lock directory: %w", err)
	}
	if usingDefaultPath {
		if err := os.Chmod(lockDir, 0o700); err != nil {
			return fmt.Errorf("secure OAuth session lock directory: %w", err)
		}
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("open OAuth session lock: refusing symlink %s", path)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect OAuth session lock: %w", statErr)
		}
	}

	fileLock := flock.New(path)
	locked, err := fileLock.TryLockContext(ctx, lockRetry)
	if err != nil {
		return fmt.Errorf("lock OAuth session: %w", err)
	}
	if !locked {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("lock OAuth session: %w", ctxErr)
		}
		return fmt.Errorf("lock OAuth session: lock was not acquired")
	}
	defer func() {
		if unlockErr := fileLock.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %w", ErrSessionUnlock, unlockErr))
		}
	}()
	return fn()
}

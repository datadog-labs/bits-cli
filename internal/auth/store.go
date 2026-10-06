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
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const (
	keyringService = "com.datadog.bits-cli"
	keyringAccount = "oauth-session"

	// configDirName is the Bits CLI configuration directory under the user's
	// home. sessionLockFileName and sessionFileName live inside it.
	configDirName       = ".bits-cli"
	sessionLockFileName = "oauth-session.lock"
	sessionFileName     = "oauth-session.json"

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

func sessionFromToken(cfg SiteConfig, token *oauth2.Token) Session {
	expiry := token.Expiry
	if expiry.IsZero() {
		// expires_in is optional in OAuth. A bounded default avoids treating the
		// token as valid forever or rotating it again on every request.
		expiry = time.Now().Add(2 * refreshWindow)
	}
	return Session{
		Site:         cfg.Site,
		ClientID:     cfg.ClientID,
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: token.RefreshToken,
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

// withSessionLock serializes every durable session transition. Store's
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

func reconcileStoreLocked(store CredentialStore) error {
	builtIn, ok := store.(Store)
	if ok {
		return builtIn.reconcileLocked()
	}
	return nil
}

func loadSessionsForDeleteLocked(store CredentialStore) ([]Session, error) {
	builtIn, ok := store.(Store)
	if ok {
		return builtIn.loadSessionsLocked()
	}
	session, err := store.Load()
	if err != nil {
		return nil, err
	}
	return []Session{session}, nil
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

// DefaultStore returns the credential store for this host. The OS keyring is
// authoritative whenever it is reachable. The file is a Linux fallback only:
// while holding the session lock, a file-only session is promoted to the
// keyring when it returns, and a duplicate file is removed.
func DefaultStore() Store {
	return Store{}
}

// Store is the single Bits CLI credential store. It persists the active session
// in the native OS credential manager (Keychain, Secret Service, or Windows
// Credential Manager) when one is available, and otherwise (Linux hosts with no
// Secret Service) in a mode-0600 JSON file under the config directory.
type Store struct {
	// lockPath overrides the cross-process flock path (tests). Empty uses the
	// default in the user's config directory; the lock holds no credential.
	lockPath string
	// filePath overrides the fallback credential file (tests). Empty uses the
	// default ~/.bits-cli/oauth-session.json.
	filePath string
	// available overrides the keyring probe (tests). nil uses the cached
	// KeyringAvailable result for this process.
	available func() bool
}

func (s Store) keyringAvailable() bool {
	if s.available != nil {
		return s.available()
	}
	return keyringAvailableCached()
}

// resolveFilePath returns the fallback credential path and whether it is the
// default location that warrants symlink and permission hardening.
func (s Store) resolveFilePath() (path string, secure bool, err error) {
	if s.filePath != "" {
		return s.filePath, false, nil
	}
	path, err = defaultSessionFilePath()
	return path, true, err
}

// Load returns the active session. With a keyring present it reads the keyring
// first, then falls back to the file only when the keyring has no session.
// Callers performing a session transition reconcile the two backends while
// holding the session lock; Load itself remains read-only so it cannot race a
// concurrent login or refresh.
func (s Store) Load() (Session, error) {
	if s.keyringAvailable() {
		session, err := keyringLoad()
		if err == nil {
			return session, nil
		}
		if !errors.Is(err, ErrNoSession) {
			return Session{}, err
		}
		// Keyring reachable but empty: adopt a pre-availability file session.
	}
	path, secure, err := s.resolveFilePath()
	if err != nil {
		return Session{}, err
	}
	return loadSessionFile(path, secure)
}

// Save writes session to the keyring when one is available, removing any
// plaintext file left from a prior fallback so a valid refresh token does not
// linger; otherwise it writes the file fallback.
func (s Store) Save(session Session) error {
	if err := session.validate(); err != nil {
		return err
	}
	path, secure, err := s.resolveFilePath()
	if err != nil {
		return err
	}
	if s.keyringAvailable() {
		if err := keyringSave(session); err != nil {
			return err
		}
		// Best-effort: the keyring copy is authoritative, so a lingering plaintext
		// file is a security nuisance, not a reason to fail an otherwise durable save.
		_ = deleteSessionFile(path)
		return nil
	}
	return saveSessionFile(path, secure, session)
}

// Delete removes the session from both backends so a change in keyring
// availability between invocations cannot leave an orphaned grant behind.
func (s Store) Delete() error {
	var errs []error
	// Always attempt the keyring delete, even when this invocation's probe
	// reports the keyring unavailable: a credential saved during an earlier
	// available invocation must not be left behind to reactivate once the
	// keyring is reachable again. keyringDelete treats a missing entry as
	// success, so this is a no-op when no keyring session exists.
	if err := keyringDelete(); err != nil {
		errs = append(errs, err)
	}
	if path, _, err := s.resolveFilePath(); err != nil {
		errs = append(errs, err)
	} else if err := deleteSessionFile(path); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// reconcileLocked applies the keyring-authoritative policy. It must only be
// called while Store's session lock is held: promoting a file session from an
// unlocked Load could overwrite a concurrent login in the keyring.
func (s Store) reconcileLocked() error {
	if !s.keyringAvailable() {
		return nil
	}

	_, keyringErr := keyringLoad()
	if keyringErr == nil {
		// A reachable keyring wins over an older fallback. Ignore cleanup errors:
		// the keyring session is durable and plaintext removal can be retried by a
		// later transition.
		if path, _, err := s.resolveFilePath(); err == nil {
			_ = deleteSessionFile(path)
		}
		return nil
	}
	if errors.Is(keyringErr, ErrSessionCorrupt) {
		// Reconciliation has no safe repair for a credential it cannot trust; let
		// Store.Load surface ErrSessionCorrupt so Login can replace it.
		return nil
	}
	if !errors.Is(keyringErr, ErrNoSession) {
		return keyringErr
	}

	path, secure, err := s.resolveFilePath()
	if err != nil {
		return err
	}
	fileSession, err := loadSessionFile(path, secure)
	if errors.Is(err, ErrNoSession) {
		return nil
	}
	if errors.Is(err, ErrSessionCorrupt) {
		// A corrupt fallback file cannot be promoted; let Store.Load surface it.
		return nil
	}
	if err != nil {
		return err
	}
	if err := keyringSave(fileSession); err != nil {
		return err
	}
	_ = deleteSessionFile(path)
	return nil
}

// loadSessionsLocked returns the readable, distinct local sessions before
// logout removes them. It does not reconcile: both grants must remain
// available until each can be considered for best-effort remote revocation.
func (s Store) loadSessionsLocked() ([]Session, error) {
	var sessions []Session
	var errs []error
	appendSession := func(session Session) {
		for _, existing := range sessions {
			if sameSession(existing, session) {
				return
			}
		}
		sessions = append(sessions, session)
	}

	if s.keyringAvailable() {
		session, err := keyringLoad()
		if err == nil {
			appendSession(session)
		} else if errors.Is(err, ErrNoSession) {
			// No keyring session is normal when this host previously used the file
			// fallback.
		} else if errors.Is(err, ErrSessionCorrupt) {
			errs = append(errs, err)
		} else {
			return nil, err
		}
	}

	path, secure, err := s.resolveFilePath()
	if err != nil {
		return nil, err
	}
	session, err := loadSessionFile(path, secure)
	if err == nil {
		appendSession(session)
	} else if errors.Is(err, ErrNoSession) {
		// Neither backend has a session.
	} else if errors.Is(err, ErrSessionCorrupt) {
		errs = append(errs, err)
	} else {
		return nil, err
	}

	if len(sessions) == 0 && len(errs) == 0 {
		return nil, ErrNoSession
	}
	return sessions, errors.Join(errs...)
}

func keyringLoad() (Session, error) {
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

func keyringSave(session Session) error {
	raw, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode OAuth session: %w", err)
	}
	if err := keyring.Set(keyringService, keyringAccount, string(raw)); err != nil {
		return fmt.Errorf("write OAuth session to OS credential store: %w", err)
	}
	return nil
}

func keyringDelete() error {
	err := keyring.Delete(keyringService, keyringAccount)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete OAuth session from OS credential store: %w", err)
	}
	return nil
}

// keyringAvailableCached probes the OS keyring once per process. Availability
// does not change within a short-lived CLI invocation, so a single probe keeps
// the store's backend choice stable and avoids repeated D-Bus round trips.
var keyringAvailableCached = sync.OnceValue(KeyringAvailable)

func defaultConfigDir() (string, error) {
	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("locate Bits CLI config directory: %w", err)
	}
	if currentUser.HomeDir == "" {
		return "", fmt.Errorf("locate Bits CLI config directory: home directory is empty")
	}
	return filepath.Join(currentUser.HomeDir, configDirName), nil
}

func defaultSessionLockPath() (string, error) {
	dir, err := defaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionLockFileName), nil
}

func (s Store) WithSessionLock(ctx context.Context, fn func() error) error {
	path := s.lockPath
	usingDefaultPath := path == ""
	if usingDefaultPath {
		var pathErr error
		path, pathErr = defaultSessionLockPath()
		if pathErr != nil {
			return pathErr
		}
	}
	return withFileLock(ctx, path, usingDefaultPath, fn)
}

// withFileLock runs fn while holding the cross-process flock at lockPath. When
// secure is true it refuses symlinked lock dirs/files and tightens dir perms.
func withFileLock(ctx context.Context, path string, secure bool, fn func() error) (err error) {
	lockDir := filepath.Dir(path)
	if secure {
		if info, statErr := os.Lstat(lockDir); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("create OAuth session lock directory: refusing symlink %s", lockDir)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect OAuth session lock directory: %w", statErr)
		}
	}
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return fmt.Errorf("create OAuth session lock directory: %w", err)
	}
	if secure {
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

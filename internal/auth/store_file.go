package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The file fallback persists the session as a mode-0600 JSON file. It has no
// at-rest encryption, so it is only reached through Store on hosts without a
// usable OS credential manager (Linux with no Secret Service). These helpers
// operate on an explicit path; Store owns path resolution and backend choice.

func defaultSessionFilePath() (string, error) {
	dir, err := defaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionFileName), nil
}

// loadSessionFile reads the credential file at path. When secure is true it
// refuses a symlinked file. The group/other-readable rejection always applies,
// since such a credential may have leaked.
func loadSessionFile(path string, secure bool) (Session, error) {
	info, statErr := os.Lstat(path)
	if errors.Is(statErr, os.ErrNotExist) {
		return Session{}, ErrNoSession
	}
	if statErr != nil {
		return Session{}, fmt.Errorf("inspect OAuth session file: %w", statErr)
	}
	if secure && info.Mode()&os.ModeSymlink != 0 {
		return Session{}, fmt.Errorf("read OAuth session file: refusing symlink %s", path)
	}
	if info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
		return Session{}, fmt.Errorf("%w: OAuth session file %s is accessible by other users (%#o)", ErrSessionCorrupt, path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Session{}, fmt.Errorf("read OAuth session file: %w", err)
	}
	var session Session
	if err := json.Unmarshal(raw, &session); err != nil {
		return Session{}, fmt.Errorf("%w: decode credential file: %w", ErrSessionCorrupt, err)
	}
	if err := session.validate(); err != nil {
		return Session{}, fmt.Errorf("%w: %w", ErrSessionCorrupt, err)
	}
	return session, nil
}

// saveSessionFile atomically writes session to path with owner-only
// permissions. When secure is true it hardens the parent directory and refuses
// symlinks; callers pass secure=false only for test-controlled paths.
func saveSessionFile(path string, secure bool, session Session) error {
	if err := session.validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if secure {
		if info, statErr := os.Lstat(dir); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("create OAuth session directory: refusing symlink %s", dir)
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect OAuth session directory: %w", statErr)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create OAuth session directory: %w", err)
	}
	if secure {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure OAuth session directory: %w", err)
		}
		if err := refuseSymlink(path, "write"); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode OAuth session: %w", err)
	}

	// Temp-file + rename so a crash never leaves a truncated credential.
	// CreateTemp opens the file 0600, and umask only clears bits, so no chmod.
	tmp, err := os.CreateTemp(dir, ".oauth-session-*.tmp")
	if err != nil {
		return fmt.Errorf("create OAuth session temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write OAuth session temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("flush OAuth session temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("commit OAuth session file: %w", err)
	}
	return nil
}

// deleteSessionFile removes the credential file at path, treating an absent
// file as success.
func deleteSessionFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete OAuth session file: %w", err)
	}
	return nil
}

func refuseSymlink(path, verb string) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s OAuth session file: refusing symlink %s", verb, path)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect OAuth session file: %w", err)
	}
	return nil
}

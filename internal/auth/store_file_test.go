package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fileTestSession() Session {
	return Session{
		Site:         "https://api.datadoghq.com",
		ClientID:     "client",
		AccessToken:  "access",
		TokenType:    "Bearer",
		RefreshToken: "refresh",
	}
}

func tempSessionPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "oauth-session.json")
}

func TestSessionFileRoundTrips(t *testing.T) {
	path := tempSessionPath(t)
	want := fileTestSession()
	want.AccessToken = "opaque-access-token"
	if err := saveSessionFile(path, false, want); err != nil {
		t.Fatalf("saveSessionFile: %v", err)
	}
	got, err := loadSessionFile(path, false)
	if err != nil {
		t.Fatalf("loadSessionFile: %v", err)
	}
	if !sameSession(got, want) {
		t.Errorf("loadSessionFile = %+v, want %+v", got, want)
	}
}

func TestLoadSessionFileMissingIsNoSession(t *testing.T) {
	if _, err := loadSessionFile(tempSessionPath(t), false); !errors.Is(err, ErrNoSession) {
		t.Fatalf("loadSessionFile = %v, want ErrNoSession", err)
	}
}

func TestSaveSessionFileWritesOwnerOnlyPermissions(t *testing.T) {
	path := tempSessionPath(t)
	if err := saveSessionFile(path, false, fileTestSession()); err != nil {
		t.Fatalf("saveSessionFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %o, want 600", perm)
	}
}

func TestDeleteSessionFileIsIdempotent(t *testing.T) {
	path := tempSessionPath(t)
	if err := deleteSessionFile(path); err != nil {
		t.Fatalf("deleteSessionFile on empty: %v", err)
	}
	if err := saveSessionFile(path, false, fileTestSession()); err != nil {
		t.Fatalf("saveSessionFile: %v", err)
	}
	if err := deleteSessionFile(path); err != nil {
		t.Fatalf("deleteSessionFile: %v", err)
	}
	if _, err := loadSessionFile(path, false); !errors.Is(err, ErrNoSession) {
		t.Fatalf("loadSessionFile after delete = %v, want ErrNoSession", err)
	}
}

func TestLoadSessionFileCorruptIsSessionCorrupt(t *testing.T) {
	path := tempSessionPath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	if _, err := loadSessionFile(path, false); !errors.Is(err, ErrSessionCorrupt) {
		t.Fatalf("loadSessionFile = %v, want ErrSessionCorrupt", err)
	}
}

func TestLoadSessionFileRejectsGroupOrOtherReadable(t *testing.T) {
	path := tempSessionPath(t)
	if err := saveSessionFile(path, false, fileTestSession()); err != nil {
		t.Fatalf("saveSessionFile: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if _, err := loadSessionFile(path, false); !errors.Is(err, ErrSessionCorrupt) {
		t.Fatalf("loadSessionFile = %v, want ErrSessionCorrupt", err)
	}
}

func TestSaveSessionFileRejectsInvalidSession(t *testing.T) {
	path := tempSessionPath(t)
	if err := saveSessionFile(path, false, Session{Site: "https://api.datadoghq.com"}); err == nil {
		t.Fatal("saveSessionFile accepted an incomplete session")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid save left a file behind: %v", err)
	}
}

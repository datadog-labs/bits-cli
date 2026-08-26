package auth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

func TestMutationRetriesAcceptCommittedPostconditions(t *testing.T) {
	session := expiredSession()

	t.Run("save committed before error", func(t *testing.T) {
		store := &memoryStore{saveCommitErr: errors.New("lost save response")}
		if err := saveWithRetry(context.Background(), store, session); err != nil {
			t.Fatal(err)
		}
		if !store.present || !sameSession(store.session, session) || store.saves != 1 {
			t.Fatalf("stored = %#v, saves = %d", store.session, store.saves)
		}
	})

	t.Run("delete committed before error", func(t *testing.T) {
		store := newMemoryStore(session)
		store.deleteCommitErr = errors.New("lost delete response")
		if err := deleteWithRetry(context.Background(), store); err != nil {
			t.Fatal(err)
		}
		if store.present || store.deletes != 1 {
			t.Fatalf("present = %v, deletes = %d", store.present, store.deletes)
		}
	})
}

func TestDefaultLockPathIgnoresProcessHomeEnvironment(t *testing.T) {
	before, err := defaultSessionLockPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	after, err := defaultSessionLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("lock path changed with process environment: %q != %q", before, after)
	}
}

func TestStoreLockSerializesProcesses(t *testing.T) {
	if os.Getenv("BITS_LOCK_HELPER") == "1" {
		store := Store{lockPath: os.Getenv("BITS_LOCK_PATH")}
		err := store.WithSessionLock(context.Background(), func() error {
			if err := os.WriteFile(os.Getenv("BITS_LOCK_READY"), []byte("ready"), 0o600); err != nil {
				return err
			}
			time.Sleep(400 * time.Millisecond)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")
	readyPath := filepath.Join(dir, "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockSerializesProcesses$")
	cmd.Env = append(os.Environ(),
		"BITS_LOCK_HELPER=1",
		"BITS_LOCK_PATH="+lockPath,
		"BITS_LOCK_READY="+readyPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cmd.Wait(); err != nil {
			t.Errorf("lock helper: %v", err)
		}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock helper did not acquire lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	started := time.Now()
	store := Store{lockPath: lockPath}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := store.WithSessionLock(ctx, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 250*time.Millisecond {
		t.Fatalf("second process acquired lock too early after %s", elapsed)
	} else {
		t.Logf("second process waited %s", elapsed)
	}
}

// newTestStore returns a Store isolated to a temp directory with a mocked,
// empty keyring and a fixed availability answer, so the keyring/file
// reconciliation can be exercised deterministically on any host.
func newTestStore(t *testing.T, keyringUp bool) Store {
	t.Helper()
	keyring.MockInit()
	dir := t.TempDir()
	return Store{
		lockPath:  filepath.Join(dir, "oauth-session.lock"),
		filePath:  filepath.Join(dir, "oauth-session.json"),
		available: func() bool { return keyringUp },
	}
}

func TestStoreImplementsCredentialStoreAndLocker(t *testing.T) {
	var _ CredentialStore = Store{}
	var _ sessionLocker = Store{}
}

func TestStorePrefersKeyringWhenAvailable(t *testing.T) {
	store := newTestStore(t, true)
	want := fileTestSession()
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(store.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Save wrote a plaintext file while the keyring was present: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !sameSession(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

// A session written to the file before a keyring appeared must stay visible
// once the keyring is available but still empty.
func TestStoreLoadAdoptsFileSessionWhenKeyringEmpty(t *testing.T) {
	store := newTestStore(t, true)
	if err := saveSessionFile(store.filePath, false, fileTestSession()); err != nil {
		t.Fatalf("seed file session: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !sameSession(got, fileTestSession()) {
		t.Errorf("Load did not adopt the file session: %+v", got)
	}
}

func TestStoreReconcilePromotesFileSessionWhenKeyringReturns(t *testing.T) {
	store := newTestStore(t, true)
	want := fileTestSession()
	if err := saveSessionFile(store.filePath, false, want); err != nil {
		t.Fatalf("seed file session: %v", err)
	}
	if err := store.WithSessionLock(context.Background(), func() error {
		return reconcileStoreLocked(store)
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, err := keyringLoad()
	if err != nil {
		t.Fatalf("load keyring session: %v", err)
	}
	if !sameSession(got, want) {
		t.Errorf("keyring session = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(store.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconcile left a plaintext file behind: %v", err)
	}
}

func TestStoreReconcileKeepsKeyringSessionAndRemovesFile(t *testing.T) {
	store := newTestStore(t, true)
	keyringSession := fileTestSession()
	keyringSession.AccessToken = "keyring-access"
	keyringSession.RefreshToken = "keyring-refresh"
	fileSession := fileTestSession()
	fileSession.AccessToken = "file-access"
	fileSession.RefreshToken = "file-refresh"
	if err := keyringSave(keyringSession); err != nil {
		t.Fatalf("seed keyring session: %v", err)
	}
	if err := saveSessionFile(store.filePath, false, fileSession); err != nil {
		t.Fatalf("seed file session: %v", err)
	}
	if err := store.WithSessionLock(context.Background(), func() error {
		return reconcileStoreLocked(store)
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !sameSession(got, keyringSession) {
		t.Errorf("Load = %+v, want keyring session %+v", got, keyringSession)
	}
	if _, err := os.Stat(store.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconcile left a plaintext file behind: %v", err)
	}
}

func TestStoreLoadEmptyBackendsIsNoSession(t *testing.T) {
	store := newTestStore(t, true)
	if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Load = %v, want ErrNoSession", err)
	}
}

// Saving with the keyring available cleans up a leftover plaintext credential.
func TestStoreSaveRemovesLingeringPlaintextFile(t *testing.T) {
	store := newTestStore(t, true)
	if err := saveSessionFile(store.filePath, false, fileTestSession()); err != nil {
		t.Fatalf("seed file session: %v", err)
	}
	if err := store.Save(fileTestSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(store.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Save left a plaintext file behind: %v", err)
	}
}

func TestStoreUsesFileWhenKeyringUnavailable(t *testing.T) {
	store := newTestStore(t, false)
	want := fileTestSession()
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(store.filePath); err != nil {
		t.Fatalf("Save did not write the file fallback: %v", err)
	}
	if _, err := keyring.Get(keyringService, keyringAccount); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring was written while unavailable: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !sameSession(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

// Delete must clear both backends so a change in keyring availability cannot
// leave an orphaned grant in the other store.
func TestStoreDeleteClearsBothBackends(t *testing.T) {
	store := newTestStore(t, true)
	if err := store.Save(fileTestSession()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := saveSessionFile(store.filePath, false, fileTestSession()); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := keyring.Get(keyringService, keyringAccount); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring session survived Delete: %v", err)
	}
	if _, err := os.Stat(store.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file session survived Delete: %v", err)
	}
}

// Delete must remove a keyring credential even when the current probe reports
// the keyring unavailable, so a session saved during an earlier available
// invocation cannot reactivate once the keyring is reachable again.
func TestStoreDeleteClearsKeyringWhenProbeReportsUnavailable(t *testing.T) {
	store := newTestStore(t, false)
	if err := keyringSave(fileTestSession()); err != nil {
		t.Fatalf("seed keyring session: %v", err)
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := keyring.Get(keyringService, keyringAccount); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keyring session survived Delete while probe reported unavailable: %v", err)
	}
}

// A corrupt keyring credential must not make reconciliation fatal: Login relies
// on reconcile returning cleanly so its own Load can surface ErrSessionCorrupt
// and replace the unusable record.
func TestStoreReconcileTreatsCorruptKeyringAsNothingToReconcile(t *testing.T) {
	store := newTestStore(t, true)
	if err := keyring.Set(keyringService, keyringAccount, "not-json"); err != nil {
		t.Fatalf("seed corrupt keyring session: %v", err)
	}
	if _, err := keyringLoad(); !errors.Is(err, ErrSessionCorrupt) {
		t.Fatalf("seed did not produce a corrupt session: %v", err)
	}
	if err := store.WithSessionLock(context.Background(), func() error {
		return reconcileStoreLocked(store)
	}); err != nil {
		t.Fatalf("reconcile returned a fatal error for a corrupt keyring session: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrSessionCorrupt) {
		t.Fatalf("Load = %v, want ErrSessionCorrupt after reconcile", err)
	}
}

func TestStoreWorksThroughSessionLock(t *testing.T) {
	store := newTestStore(t, false)
	err := withSessionLock(context.Background(), store, func() error {
		return store.Save(fileTestSession())
	})
	if err != nil {
		t.Fatalf("withSessionLock: %v", err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

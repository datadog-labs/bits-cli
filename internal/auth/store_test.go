package auth

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestKeyringStoreLockSerializesProcesses(t *testing.T) {
	if os.Getenv("BITS_LOCK_HELPER") == "1" {
		store := KeyringStore{LockPath: os.Getenv("BITS_LOCK_PATH")}
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
	cmd := exec.Command(os.Args[0], "-test.run=^TestKeyringStoreLockSerializesProcesses$")
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
	store := KeyringStore{LockPath: lockPath}
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

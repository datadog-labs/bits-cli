//go:build unix

package tools

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSafeReplaceHonorsUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := t.TempDir()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	// A new file must honor umask (0644 &^ 077 == 0600), not be forced to 0644.
	if err := safeReplace(context.Background(), r, "new.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("new file mode = %o, want 0600 (umask must be honored)", got)
	}

	// An existing file's exact bits are preserved regardless of umask.
	existing := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o640); err != nil { // exact bits, ignoring umask
		t.Fatal(err)
	}
	if err := safeReplace(context.Background(), r, "keep.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("overwritten file mode = %o, want 0640 (existing perms preserved)", got)
	}
}

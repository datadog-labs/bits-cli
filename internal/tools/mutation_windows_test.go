package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSafeReplaceKeepsWindowsAttributes(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hidden.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(target)
	if err != nil {
		t.Fatal(err)
	}
	const want = windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM
	if err := windows.SetFileAttributes(name, want); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	if err := safeReplace(context.Background(), r, "hidden.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		t.Fatal(err)
	}
	if attrs&want != want {
		t.Fatalf("attributes = %#x, want hidden and system kept", attrs)
	}
}

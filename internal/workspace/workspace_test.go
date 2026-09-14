package workspace

import (
	"path/filepath"
	"testing"
)

func TestOpenUsesAbsoluteConfinedRoot(t *testing.T) {
	dir := t.TempDir()
	workspace, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })

	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Path() != want {
		t.Fatalf("path = %q, want %q", workspace.Path(), want)
	}
	if _, err := workspace.FS().Open("../outside"); err == nil {
		t.Fatal("confined filesystem allowed parent traversal")
	}
}

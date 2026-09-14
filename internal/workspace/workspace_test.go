package workspace

import (
	"os"
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

func TestOpenStoresUserRelativeDisplayPath(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "go", "src", "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })

	if got, want := ws.DisplayPath(), filepath.Join("~", "go", "src", "repo"); got != want {
		t.Fatalf("display path = %q, want %q", got, want)
	}
}

func TestUserRelativePathOnlyShortensHomeDescendants(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "bits")
	inside := filepath.Join(home, "go", "src", "repo")
	outside := filepath.Join(string(filepath.Separator), "Users", "other", "repo")

	if got, want := userRelativePath(inside, home), filepath.Join("~", "go", "src", "repo"); got != want {
		t.Fatalf("inside path = %q, want %q", got, want)
	}
	if got := userRelativePath(home, home); got != "~" {
		t.Fatalf("home path = %q, want ~", got)
	}
	if got := userRelativePath(outside, home); got != outside {
		t.Fatalf("outside path = %q, want %q", got, outside)
	}
}

package workspace

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLocalPath(t *testing.T) {
	root := t.TempDir()
	type result struct{ path, err string }
	same := func(r result) [2]result { return [2]result{r, r} }
	for _, test := range []struct {
		name string
		path string
		// want holds the Unix result, then the Windows one.
		want [2]result
	}{
		{name: "relative", path: "sub/a.txt", want: same(result{path: "sub/a.txt"})},
		{name: "dot", path: ".", want: same(result{path: "."})},
		{name: "cleaned", path: "./sub//x/../a.txt", want: same(result{path: "sub/a.txt"})},
		{name: "empty", path: "", want: same(result{err: "must not be empty"})},
		{name: "backslashes", path: `sub\dir\b.txt`, want: [2]result{{path: `sub\dir\b.txt`}, {path: "sub/dir/b.txt"}}},
		{name: "dot backslash", path: `.\a.txt`, want: [2]result{{path: `.\a.txt`}, {path: "a.txt"}}},
		{name: "escape", path: "../x", want: same(result{err: "outside the workspace"})},
		{name: "backslash escape", path: `..\x`, want: [2]result{{path: `..\x`}, {err: "outside the workspace"}}},
		{name: "rooted", path: "/etc/passwd", want: same(result{err: "not absolute"})},
		{name: "absolute inside", path: filepath.Join(root, "sub", "a.txt"), want: same(result{err: `not absolute: use "sub/a.txt"`})},
		{name: "drive relative", path: "C:a.txt", want: [2]result{{path: "C:a.txt"}, {err: "not absolute"}}},
		{name: "drive slash", path: "C:/Windows/win.ini", want: [2]result{{path: "C:/Windows/win.ini"}, {err: "not absolute"}}},
		{name: "rooted backslash", path: `\qa\a.txt`, want: [2]result{{path: `\qa\a.txt`}, {err: "not absolute"}}},
		{name: "trailing dot and space", path: "trailing. ", want: [2]result{{path: "trailing. "}, {path: "trailing"}}},
		{name: "device name", path: "CON", want: [2]result{{path: "CON"}, {err: "not a valid file name"}}},
		{name: "stream", path: "a.txt:s", want: [2]result{{path: "a.txt:s"}, {err: "not a valid file name"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := test.want[0]
			if filepath.Separator == '\\' {
				want = test.want[1]
			}
			got, err := LocalPath(root, test.path)
			if want.err != "" {
				if err == nil || !strings.Contains(err.Error(), want.err) {
					t.Fatalf("LocalPath(%q) = %q, %v; want error containing %q", test.path, got, err, want.err)
				}
				return
			}
			if err != nil || got != want.path {
				t.Fatalf("LocalPath(%q) = %q, %v; want %q", test.path, got, err, want.path)
			}
		})
	}
}

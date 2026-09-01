package tools

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

type deniedIgnoreFS struct{ fs.FS }

func (fsys deniedIgnoreFS) Open(name string) (fs.File, error) {
	if name == ".gitignore" {
		return nil, fs.ErrPermission
	}
	return fsys.FS.Open(name)
}

func TestIgnorerIgnore(t *testing.T) {
	tests := []struct {
		name    string
		content string
		base    string
		path    string
		isDir   bool
		want    bool
	}{
		// Non-anchored: match basename at any depth.
		{"star root", "*.log", ".", "foo.log", false, true},
		{"star nested", "*.log", ".", "src/foo.log", false, true},
		{"star no match", "*.log", ".", "foo.txt", false, false},

		// Directory-only patterns.
		{"dir-only matches dir", "build/", ".", "build", true, true},
		{"dir-only skips file", "build/", ".", "build", false, false},
		{"dir-only nested", "build/", ".", "src/build", true, true},

		// Anchored with leading slash.
		{"anchored root only", "/dist", ".", "dist", false, true},
		{"anchored not nested", "/dist", ".", "src/dist", false, false},

		// Double-star.
		{"doublestar any depth", "**/*.go", ".", "src/main.go", false, true},
		{"doublestar root level", "**/*.go", ".", "main.go", false, true},
		{"doublestar deep", "**/*.go", ".", "a/b/c/main.go", false, true},
		{"doublestar prefix", "vendor/**", ".", "vendor/pkg/file.go", false, true},
		{"doublestar prefix no match", "vendor/**", ".", "notvendor/file.go", false, false},

		// Wildmatch syntax and escaping.
		{"character class", "file[0-9].log", ".", "file3.log", false, true},
		{"character class no match", "file[0-9].log", ".", "filex.log", false, false},
		{"escaped wildcard is literal", `\*.txt`, ".", "notes.txt", false, false},
		{"escaped wildcard matches literal", `\*.txt`, ".", "*.txt", false, true},
		{"escaped trailing space", "hello\\ ", ".", "hello ", false, true},

		// Negation: last matching rule wins.
		{"negation un-ignores", "*.log\n!important.log", ".", "important.log", false, false},
		{"negation re-ignores", "!*.log\n*.log", ".", "foo.log", false, true},
		{"negation others still ignored", "*.log\n!important.log", ".", "other.log", false, true},

		// Subdirectory .gitignore scoping.
		{"subdir match", "*.test", "src", "src/foo.test", false, true},
		{"subdir excludes parent", "*.test", "src", "foo.test", false, false},
		{"subdir excludes sibling dir", "*.test", "src", "lib/foo.test", false, false},
		{"subdir anchored match", "/local.log", "src", "src/local.log", false, true},
		{"subdir anchored excludes parent", "/local.log", "src", "local.log", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := ".gitignore"
			if tt.base != "." {
				name = tt.base + "/.gitignore"
			}
			fsys := fstest.MapFS{
				name: &fstest.MapFile{Data: []byte(tt.content)},
			}
			var ig Ignorer
			if err := ig.Add(context.Background(), fsys, tt.base); err != nil {
				t.Fatal(err)
			}
			if got := ig.Ignore(tt.path, tt.isDir); got != tt.want {
				t.Errorf("Ignore(%q, isDir=%v) = %v, want %v", tt.path, tt.isDir, got, tt.want)
			}
		})
	}
}

func TestIgnorerReportsGitignoreReadErrors(t *testing.T) {
	var ig Ignorer
	err := ig.Add(context.Background(), deniedIgnoreFS{FS: fstest.MapFS{}}, ".")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v, want permission error", err)
	}
}

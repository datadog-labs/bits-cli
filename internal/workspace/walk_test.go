package workspace

import (
	"context"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
)

func TestWalkFSAppliesNestedIgnoresAndSkipsGit(t *testing.T) {
	fsys := fstest.MapFS{
		".git/config":           {Data: []byte("internal")},
		".gitignore":            {Data: []byte("*.log\nignored/\n")},
		"error.log":             {Data: []byte("ignored")},
		"ignored/file.txt":      {Data: []byte("ignored")},
		"main.go":               {Data: []byte("visible")},
		"src/.gitignore":        {Data: []byte("*.test\n")},
		"src/main.go":           {Data: []byte("visible")},
		"src/main.test":         {Data: []byte("ignored")},
		"src/nested/readme.txt": {Data: []byte("visible")},
	}

	var paths []string
	err := WalkFS(context.Background(), fsys, ".", func(filePath string, _ fs.DirEntry) error {
		paths = append(paths, filePath)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".", ".gitignore", "main.go", "src", "src/.gitignore", "src/main.go", "src/nested", "src/nested/readme.txt"} {
		if !slices.Contains(paths, want) {
			t.Errorf("paths %v do not contain %q", paths, want)
		}
	}
	for _, excluded := range []string{".git", ".git/config", "error.log", "ignored", "ignored/file.txt", "src/main.test"} {
		if slices.Contains(paths, excluded) {
			t.Errorf("paths %v contain excluded path %q", paths, excluded)
		}
	}
}

func TestWalkFSLoadsParentIgnoresForScopedWalk(t *testing.T) {
	fsys := fstest.MapFS{
		".gitignore":      {Data: []byte("src/secret.txt\n")},
		"src/.gitignore":  {Data: []byte("*.tmp\n")},
		"src/secret.txt":  {Data: []byte("ignored")},
		"src/scratch.tmp": {Data: []byte("ignored")},
		"src/visible.txt": {Data: []byte("visible")},
	}

	var paths []string
	err := WalkFS(context.Background(), fsys, "src", func(filePath string, _ fs.DirEntry) error {
		paths = append(paths, filePath)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(paths, []string{"src", "src/.gitignore", "src/visible.txt"}) {
		t.Fatalf("paths = %v", paths)
	}
}

func TestWalkFSCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fsys := fstest.MapFS{
		"a.txt": {Data: []byte("a")},
		"b.txt": {Data: []byte("b")},
	}
	visited := 0
	err := WalkFS(ctx, fsys, ".", func(_ string, _ fs.DirEntry) error {
		visited++
		cancel()
		return nil
	})
	if err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if visited != 1 {
		t.Fatalf("visited = %d, want 1", visited)
	}
}

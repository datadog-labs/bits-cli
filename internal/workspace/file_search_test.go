package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileSearchSessionWalksOnceAcrossQueries(t *testing.T) {
	var walks atomic.Int32
	session := newFileSearchSession(context.Background(), func(_ context.Context, visit WalkFunc) error {
		walks.Add(1)
		for _, filePath := range []string{"alpha.go", "beta.go", "docs/alphabet.md"} {
			if err := visit(filePath, fileSearchTestEntry(filepath.Base(filePath))); err != nil {
				return err
			}
		}
		return visit("alpha-link.go", fileSearchTestSymlinkEntry("alpha-link.go"))
	}, FileSearchOptions{Limit: 10})
	t.Cleanup(session.Close)

	alphaGeneration := session.UpdateQuery("alpha")
	alpha := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == alphaGeneration && snapshot.Complete
	})
	if got := fileSearchResultPaths(alpha.Results); !slices.Equal(got, []string{"alpha.go", "docs/alphabet.md"}) {
		t.Fatalf("alpha results = %v", got)
	}

	betaGeneration := session.UpdateQuery("beta")
	beta := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == betaGeneration && snapshot.Complete
	})
	if got := fileSearchResultPaths(beta.Results); !slices.Equal(got, []string{"beta.go"}) {
		t.Fatalf("beta results = %v", got)
	}
	if got := walks.Load(); got != 1 {
		t.Fatalf("walks = %d, want 1", got)
	}
}

func TestFileSearchSessionStreamsPartialThenFinalSnapshot(t *testing.T) {
	firstIndexed := make(chan struct{})
	finishWalk := make(chan struct{})
	session := newFileSearchSession(context.Background(), func(ctx context.Context, visit WalkFunc) error {
		if err := visit("first.go", fileSearchTestEntry("first.go")); err != nil {
			return err
		}
		close(firstIndexed)
		select {
		case <-finishWalk:
		case <-ctx.Done():
			return ctx.Err()
		}
		return visit("second.go", fileSearchTestEntry("second.go"))
	}, FileSearchOptions{})
	t.Cleanup(session.Close)

	generation := session.UpdateQuery("go")
	<-firstIndexed
	partial := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == generation && !snapshot.Complete && snapshot.CandidateCount == 1
	})
	if len(partial.Results) != 1 || partial.Results[0].Path != "first.go" {
		t.Fatalf("partial results = %+v", partial.Results)
	}

	close(finishWalk)
	final := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == generation && snapshot.Complete
	})
	if final.CandidateCount != 2 {
		t.Fatalf("final candidate count = %d, want 2", final.CandidateCount)
	}
	if got := fileSearchResultPaths(final.Results); !slices.Equal(got, []string{"first.go", "second.go"}) {
		t.Fatalf("final results = %v", got)
	}
}

func TestFileSearchSessionStampsQueryGenerations(t *testing.T) {
	finishWalk := make(chan struct{})
	session := newFileSearchSession(context.Background(), func(ctx context.Context, visit WalkFunc) error {
		if err := visit("alpha.go", fileSearchTestEntry("alpha.go")); err != nil {
			return err
		}
		select {
		case <-finishWalk:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, FileSearchOptions{})
	t.Cleanup(session.Close)

	alphaGeneration := session.UpdateQuery("alpha")
	alpha := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == alphaGeneration && snapshot.CandidateCount == 1
	})
	betaGeneration := session.UpdateQuery("beta")
	beta := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == betaGeneration
	})

	if alpha.Query != "alpha" || beta.Query != "beta" {
		t.Fatalf("queries = %q then %q", alpha.Query, beta.Query)
	}
	if betaGeneration <= alphaGeneration {
		t.Fatalf("generations = %d then %d", alphaGeneration, betaGeneration)
	}
	if len(beta.Results) != 0 {
		t.Fatalf("beta results = %+v, want none", beta.Results)
	}
	close(finishWalk)
}

func TestFileSearchSessionUsesWorkspaceTraversal(t *testing.T) {
	dir := t.TempDir()
	writeFileSearchTestFile(t, dir, ".git/config", "internal")
	writeFileSearchTestFile(t, dir, ".gitignore", "*.log\nignored/\n")
	writeFileSearchTestFile(t, dir, "ignored/secret.go", "ignored")
	writeFileSearchTestFile(t, dir, "debug.log", "ignored")
	writeFileSearchTestFile(t, dir, "main.go", "visible")
	writeFileSearchTestFile(t, dir, "src/.gitignore", "generated.go\n")
	writeFileSearchTestFile(t, dir, "src/generated.go", "ignored")
	writeFileSearchTestFile(t, dir, "src/visible.go", "visible")
	if err := os.Symlink(filepath.Join(dir, "main.go"), filepath.Join(dir, "linked.go")); err != nil {
		t.Logf("symlinks unavailable: %v", err)
	}

	workspace, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	session := workspace.NewFileSearchSession(context.Background(), FileSearchOptions{})
	t.Cleanup(session.Close)

	generation := session.UpdateQuery("visible")
	snapshot := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == generation && snapshot.Complete
	})
	if snapshot.Err != nil {
		t.Fatal(snapshot.Err)
	}
	if snapshot.CandidateCount != 5 {
		t.Fatalf("candidate count = %d, want 5", snapshot.CandidateCount)
	}
	if got := fileSearchResultPaths(snapshot.Results); !slices.Equal(got, []string{"src/visible.go"}) {
		t.Fatalf("results = %v", got)
	}

	generation = session.UpdateQuery("src/")
	snapshot = waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == generation && snapshot.Complete
	})
	if !slices.Contains(fileSearchResultPaths(snapshot.Results), "src/") {
		t.Fatalf("directory result missing trailing slash: %v", fileSearchResultPaths(snapshot.Results))
	}
}

func TestFileSearchSessionRanksAndBoundsResults(t *testing.T) {
	session := newFileSearchSession(context.Background(), func(_ context.Context, visit WalkFunc) error {
		for _, filePath := range []string{"cmd/main.go", "domain.go", "internal/main.go", "main.go"} {
			if err := visit(filePath, fileSearchTestEntry(filepath.Base(filePath))); err != nil {
				return err
			}
		}
		return nil
	}, FileSearchOptions{Limit: 2})
	t.Cleanup(session.Close)

	generation := session.UpdateQuery("main")
	snapshot := waitForFileSearchSnapshot(t, session.Updates(), func(snapshot FileSearchSnapshot) bool {
		return snapshot.Generation == generation && snapshot.Complete
	})
	if len(snapshot.Results) != 2 {
		t.Fatalf("result count = %d, want 2", len(snapshot.Results))
	}
	if snapshot.Results[0].Path != "main.go" {
		t.Fatalf("first result = %q, want main.go", snapshot.Results[0].Path)
	}
	if snapshot.Results[0].Score < snapshot.Results[1].Score {
		t.Fatalf("scores are not descending: %+v", snapshot.Results)
	}
	if got := fileSearchLimit(MaximumFileSearchLimit + 1); got != MaximumFileSearchLimit {
		t.Fatalf("capped limit = %d, want %d", got, MaximumFileSearchLimit)
	}
}

func TestFileSearchSessionCloseCancelsAndJoinsWalk(t *testing.T) {
	walkStarted := make(chan struct{})
	walkStopped := make(chan struct{})
	session := newFileSearchSession(context.Background(), func(ctx context.Context, _ WalkFunc) error {
		close(walkStarted)
		<-ctx.Done()
		close(walkStopped)
		return ctx.Err()
	}, FileSearchOptions{})

	generation := session.UpdateQuery("anything")
	<-walkStarted
	closed := make(chan struct{})
	go func() {
		session.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
	select {
	case <-walkStopped:
	default:
		t.Fatal("Close returned before the walker stopped")
	}
	for range session.Updates() {
	}
	if got := session.UpdateQuery("after close"); got != generation {
		t.Fatalf("generation after Close = %d, want %d", got, generation)
	}
	session.Close()
}

func BenchmarkRankFileSearch60000(b *testing.B) {
	paths := make([]string, 60_000)
	for i := range paths {
		paths[i] = fmt.Sprintf("internal/component%03d/package%03d/file%05d.go", i%200, i%1000, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rankFileSearch("cmp42file", paths, 10)
	}
}

type fileSearchTestEntry string

func (e fileSearchTestEntry) Name() string    { return string(e) }
func (fileSearchTestEntry) IsDir() bool       { return false }
func (fileSearchTestEntry) Type() fs.FileMode { return 0 }
func (e fileSearchTestEntry) Info() (fs.FileInfo, error) {
	return fileSearchTestFileInfo{name: string(e)}, nil
}

type fileSearchTestFileInfo struct {
	name string
	mode fs.FileMode
}

func (i fileSearchTestFileInfo) Name() string      { return i.name }
func (fileSearchTestFileInfo) Size() int64         { return 0 }
func (i fileSearchTestFileInfo) Mode() fs.FileMode { return i.mode }
func (fileSearchTestFileInfo) ModTime() time.Time  { return time.Time{} }
func (i fileSearchTestFileInfo) IsDir() bool       { return i.mode.IsDir() }
func (fileSearchTestFileInfo) Sys() any            { return nil }

type fileSearchTestSymlinkEntry string

func (e fileSearchTestSymlinkEntry) Name() string             { return string(e) }
func (fileSearchTestSymlinkEntry) IsDir() bool                { return false }
func (fileSearchTestSymlinkEntry) Type() fs.FileMode          { return fs.ModeSymlink }
func (fileSearchTestSymlinkEntry) Info() (fs.FileInfo, error) { return nil, nil }

func waitForFileSearchSnapshot(t *testing.T, updates <-chan FileSearchSnapshot, match func(FileSearchSnapshot) bool) FileSearchSnapshot {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case snapshot, ok := <-updates:
			if !ok {
				t.Fatal("updates closed before matching snapshot")
			}
			if match(snapshot) {
				return snapshot
			}
		case <-timer.C:
			t.Fatal("timed out waiting for file-search snapshot")
		}
	}
}

func fileSearchResultPaths(results []FileSearchResult) []string {
	paths := make([]string, len(results))
	for i, result := range results {
		paths[i] = result.Path
	}
	return paths
}

func writeFileSearchTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

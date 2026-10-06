package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnvironmentSnapshotCapturesGitSummaryWithoutFilenames(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	writeSnapshotTestFile(t, repo, "committed.txt", "one\n")
	writeSnapshotTestFile(t, repo, "staged.txt", "one\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.name=Bits Test", "-c", "user.email=bits@example.com", "commit", "-m", "initial")

	writeSnapshotTestFile(t, repo, "committed.txt", "two\n")
	writeSnapshotTestFile(t, repo, "staged.txt", "two\n")
	runGit(t, repo, "add", "staged.txt")
	writeSnapshotTestFile(t, repo, "untracked-secret-name.txt", "three\n")

	ws := openSnapshotTestWorkspace(t, repo)
	snapshot := ws.Snapshot(context.Background())
	if snapshot.Path != ws.Path() {
		t.Fatalf("workspace path = %q, want %q", snapshot.Path, ws.Path())
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Repository.State != RepositoryPresent || snapshot.Repository.Root != resolvedRepo || snapshot.Repository.Name != filepath.Base(resolvedRepo) {
		t.Fatalf("repository = %+v", snapshot.Repository)
	}
	if snapshot.Repository.Branch != "main" || snapshot.Repository.Commit == "" || snapshot.Repository.Detached || snapshot.Repository.Unborn {
		t.Fatalf("git identity = %+v", snapshot.Repository)
	}
	if got := snapshot.Repository.Changes; !got.Known || !got.Staged || !got.Unstaged || !got.Untracked || got.Conflicted {
		t.Fatalf("changes = %+v", got)
	}
}

func TestSnapshotHandlesNonGitAndDetachedRepositories(t *testing.T) {
	t.Run("non git", func(t *testing.T) {
		ws := openSnapshotTestWorkspace(t, t.TempDir())
		snapshot := ws.Snapshot(context.Background())
		if snapshot.Repository.State != RepositoryAbsent || snapshot.Path != ws.Path() {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	})

	t.Run("detached", func(t *testing.T) {
		repo := t.TempDir()
		runGit(t, repo, "init", "-b", "main")
		writeSnapshotTestFile(t, repo, "file.txt", "one\n")
		runGit(t, repo, "add", ".")
		runGit(t, repo, "-c", "user.name=Bits Test", "-c", "user.email=bits@example.com", "commit", "-m", "initial")
		runGit(t, repo, "checkout", "--detach")

		snapshot := openSnapshotTestWorkspace(t, repo).Snapshot(context.Background())
		if snapshot.Repository.State != RepositoryPresent || !snapshot.Repository.Detached || snapshot.Repository.Branch != "" || snapshot.Repository.Commit == "" {
			t.Fatalf("detached repository = %+v", snapshot.Repository)
		}
	})
}

func TestSnapshotMarksUnbornRepositoryExplicitly(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "future")
	snapshot := openSnapshotTestWorkspace(t, repo).Snapshot(context.Background())
	if snapshot.Repository.State != RepositoryPresent || snapshot.Repository.Branch != "future" || !snapshot.Repository.Unborn || snapshot.Repository.Commit != "" {
		t.Fatalf("unborn repository = %+v", snapshot.Repository)
	}
}

func TestParsePorcelainTracksConflictWithoutRetainingPaths(t *testing.T) {
	changes := parsePorcelain([]byte("UU private/customer-name.txt\x00R  renamed.txt\x00old-secret.txt\x00?? scratch.txt\x00"))
	if !changes.Known || !changes.Conflicted || !changes.Staged || !changes.Untracked {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRepositoryStateForRootErrorOnlyCallsExplicitNonRepositoryAbsent(t *testing.T) {
	notRepository := &exec.ExitError{Stderr: []byte("fatal: not a git repository (or any of the parent directories): .git")}
	gitFailure := &exec.ExitError{Stderr: []byte("fatal: detected dubious ownership in repository")}

	if got := repositoryStateForRootError(notRepository, nil); got != RepositoryAbsent {
		t.Fatalf("explicit non-repository state = %v, want absent", got)
	}
	for name, test := range map[string]struct {
		err        error
		contextErr error
	}{
		"other git failure":  {err: gitFailure},
		"missing executable": {err: &exec.Error{Name: "git", Err: exec.ErrNotFound}},
		"deadline":           {err: gitFailure, contextErr: context.DeadlineExceeded},
		"unknown":            {err: errors.New("unexpected git failure")},
	} {
		t.Run(name, func(t *testing.T) {
			if got := repositoryStateForRootError(test.err, test.contextErr); got != RepositoryUnavailable {
				t.Fatalf("repository state = %v, want unavailable", got)
			}
		})
	}
}

func TestSnapshotUsesStableGitLocaleForNonRepositoryDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX executable script")
	}
	bin := t.TempDir()
	gitPath := filepath.Join(bin, "git")
	script := `#!/bin/sh
if [ "$LC_ALL" = "C" ]; then
	echo "fatal: not a git repository (or any of the parent directories): .git" >&2
else
	echo "fatal: pas un dépôt git" >&2
fi
exit 128
`
	if err := os.WriteFile(gitPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "fr_FR.UTF-8")

	snapshot := openSnapshotTestWorkspace(t, t.TempDir()).Snapshot(context.Background())
	if snapshot.Repository.State != RepositoryAbsent {
		t.Fatalf("repository state = %v, want absent under a localized parent environment", snapshot.Repository.State)
	}
}

func openSnapshotTestWorkspace(t *testing.T, path string) *Workspace {
	t.Helper()
	ws, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(t.Context(), "git", commandArgs...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeSnapshotTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

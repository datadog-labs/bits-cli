package status

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSystemProviderCapturesGitSummaryWithoutFilenames(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	writeTestFile(t, repo, "committed.txt", "one\n")
	writeTestFile(t, repo, "staged.txt", "one\n")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "-c", "user.name=Bits Test", "-c", "user.email=bits@example.com", "commit", "-m", "initial")

	writeTestFile(t, repo, "committed.txt", "two\n")
	writeTestFile(t, repo, "staged.txt", "two\n")
	runGit(t, repo, "add", "staged.txt")
	writeTestFile(t, repo, "untracked-secret-name.txt", "three\n")

	env := (SystemProvider{Directory: repo, Timeout: 2 * time.Second}).Collect(context.Background())
	if env.WorkingDirectory != repo {
		t.Fatalf("working directory = %q, want %q", env.WorkingDirectory, repo)
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if env.Repository.State != RepositoryPresent || env.Repository.Root != resolvedRepo || env.Repository.Name != filepath.Base(resolvedRepo) {
		t.Fatalf("repository = %+v", env.Repository)
	}
	if env.Repository.Branch != "main" || env.Repository.Commit == "" || env.Repository.Detached || env.Repository.Unborn {
		t.Fatalf("git identity = %+v", env.Repository)
	}
	if got := env.Repository.Changes; !got.Known || !got.Staged || !got.Unstaged || !got.Untracked || got.Conflicted {
		t.Fatalf("changes = %+v", got)
	}
}

func TestSystemProviderHandlesNonGitAndDetachedRepositories(t *testing.T) {
	t.Run("non git", func(t *testing.T) {
		dir := t.TempDir()
		env := (SystemProvider{Directory: dir}).Collect(context.Background())
		if env.Repository.State != RepositoryAbsent || env.WorkingDirectory != dir {
			t.Fatalf("environment = %+v", env)
		}
	})

	t.Run("detached", func(t *testing.T) {
		repo := t.TempDir()
		runGit(t, repo, "init", "-b", "main")
		writeTestFile(t, repo, "file.txt", "one\n")
		runGit(t, repo, "add", ".")
		runGit(t, repo, "-c", "user.name=Bits Test", "-c", "user.email=bits@example.com", "commit", "-m", "initial")
		runGit(t, repo, "checkout", "--detach")

		env := (SystemProvider{Directory: repo}).Collect(context.Background())
		if env.Repository.State != RepositoryPresent || !env.Repository.Detached || env.Repository.Branch != "" || env.Repository.Commit == "" {
			t.Fatalf("detached repository = %+v", env.Repository)
		}
	})
}

func TestSystemProviderMarksUnbornRepositoryExplicitly(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "future")
	env := (SystemProvider{Directory: repo}).Collect(context.Background())
	if env.Repository.State != RepositoryPresent || env.Repository.Branch != "future" || !env.Repository.Unborn || env.Repository.Commit != "" {
		t.Fatalf("unborn repository = %+v", env.Repository)
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

func TestSystemProviderUsesStableGitLocaleForNonRepositoryDetection(t *testing.T) {
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

	environment := (SystemProvider{Directory: t.TempDir(), Timeout: 10 * time.Second}).Collect(context.Background())
	if environment.Repository.State != RepositoryAbsent {
		t.Fatalf("repository state = %v, want absent under a localized parent environment", environment.Repository.State)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const snapshotTimeout = 2 * time.Second

// RepositoryState distinguishes a known non-Git workspace from a failed or
// unavailable Git inspection.
type RepositoryState uint8

const (
	RepositoryUnavailable RepositoryState = iota
	RepositoryAbsent
	RepositoryPresent
)

// Changes is a privacy-bounded working-tree summary. It deliberately retains
// no filenames or raw porcelain output.
type Changes struct {
	Known      bool
	Staged     bool
	Unstaged   bool
	Untracked  bool
	Conflicted bool
}

// Repository is the local Git identity and working-tree state for a workspace.
type Repository struct {
	State    RepositoryState
	Root     string
	Name     string
	Branch   string
	Commit   string
	Detached bool
	Unborn   bool
	Changes  Changes
}

// Environment is the local workspace and Git state observed at one point in time.
type Environment struct {
	Path       string
	Repository Repository
}

// Snapshot inspects the workspace and its containing Git repository. Git is
// invoked directly, without a shell, under one short cancellation-aware
// deadline. Independently useful fields remain populated when part of the Git
// inspection is unavailable.
func (w *Workspace) Snapshot(parent context.Context) Environment {
	snapshot := Environment{
		Path:       w.path,
		Repository: Repository{State: RepositoryUnavailable},
	}

	ctx, cancel := context.WithTimeout(parent, snapshotTimeout)
	defer cancel()

	root, err := git(ctx, w.path, "rev-parse", "--show-toplevel")
	if err != nil {
		snapshot.Repository.State = repositoryStateForRootError(err, ctx.Err())
		return snapshot
	}

	repository := Repository{
		State: RepositoryPresent,
		Root:  root,
		Name:  filepath.Base(root),
	}
	if branch, branchErr := git(ctx, w.path, "symbolic-ref", "--quiet", "--short", "HEAD"); branchErr == nil {
		repository.Branch = branch
	}
	if commit, commitErr := git(ctx, w.path, "rev-parse", "--verify", "HEAD"); commitErr == nil {
		repository.Commit = commit
		repository.Detached = repository.Branch == ""
	} else if repository.Branch != "" && ctx.Err() == nil {
		repository.Unborn = true
	}
	if raw, statusErr := gitBytes(ctx, w.path, "status", "--porcelain=v1", "-z", "--untracked-files=normal"); statusErr == nil {
		repository.Changes = parsePorcelain(raw)
	}
	snapshot.Repository = repository
	return snapshot
}

func repositoryStateForRootError(err, contextError error) RepositoryState {
	if contextError != nil {
		return RepositoryUnavailable
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && strings.Contains(strings.ToLower(string(exitError.Stderr)), "not a git repository") {
		return RepositoryAbsent
	}
	return RepositoryUnavailable
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	raw, err := gitBytes(ctx, directory, args...)
	return strings.TrimSpace(string(raw)), err
}

func gitBytes(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LC_ALL=C")
	return command.Output()
}

// parsePorcelain reduces porcelain-v1 -z records to booleans immediately.
// Rename/copy records carry a second NUL-delimited path, which is skipped.
func parsePorcelain(raw []byte) Changes {
	changes := Changes{Known: true}
	records := bytes.Split(raw, []byte{0})
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 3 {
			continue
		}
		x, y := record[0], record[1]
		switch {
		case x == '?' && y == '?':
			changes.Untracked = true
		case isConflictStatus(x, y):
			changes.Conflicted = true
		default:
			changes.Staged = changes.Staged || (x != ' ' && x != '?')
			changes.Unstaged = changes.Unstaged || (y != ' ' && y != '?')
		}
		if x == 'R' || x == 'C' {
			i++
		}
	}
	return changes
}

func isConflictStatus(x, y byte) bool {
	switch string([]byte{x, y}) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}

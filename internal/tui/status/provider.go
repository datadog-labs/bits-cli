// Package status owns the native /status screen and its bounded local
// workspace snapshot.
package status

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

const defaultCollectionTimeout = 2 * time.Second

// RepositoryState distinguishes a known non-Git directory from a failed or
// unavailable Git inspection. The UI can therefore avoid guessing.
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

// Repository is the non-secret Git identity and state rendered by /status.
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

// Environment is the local snapshot collected each time /status opens.
type Environment struct {
	WorkingDirectory string
	Repository       Repository
}

// Provider supplies a fresh environment snapshot. Implementations may perform
// local I/O and must honor cancellation.
type Provider interface {
	Collect(context.Context) Environment
}

// SystemProvider reads the current directory and invokes Git directly, without
// a shell, under one short deadline. Directory defaults to the process cwd.
type SystemProvider struct {
	Directory string
	Timeout   time.Duration
}

// Collect returns independently useful fields when part of Git inspection is
// unavailable. Command errors and stderr are never copied into the snapshot.
func (p SystemProvider) Collect(parent context.Context) Environment {
	directory := p.Directory
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return Environment{Repository: Repository{State: RepositoryUnavailable}}
		}
	}
	environment := Environment{
		WorkingDirectory: directory,
		Repository:       Repository{State: RepositoryUnavailable},
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultCollectionTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	root, err := git(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		environment.Repository.State = repositoryStateForRootError(err, ctx.Err())
		return environment
	}

	repository := Repository{
		State: RepositoryPresent,
		Root:  root,
		Name:  filepath.Base(root),
	}
	if branch, branchErr := git(ctx, directory, "symbolic-ref", "--quiet", "--short", "HEAD"); branchErr == nil {
		repository.Branch = branch
	}
	if commit, commitErr := git(ctx, directory, "rev-parse", "--verify", "HEAD"); commitErr == nil {
		repository.Commit = commit
		repository.Detached = repository.Branch == ""
	} else if repository.Branch != "" && ctx.Err() == nil {
		repository.Unborn = true
	}
	if raw, statusErr := gitBytes(ctx, directory, "status", "--porcelain=v1", "-z", "--untracked-files=normal"); statusErr == nil {
		repository.Changes = parsePorcelain(raw)
	}
	environment.Repository = repository
	return environment
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

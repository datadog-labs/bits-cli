// Package resume remembers which conversation was last open in which
// workspace, so `bits --last` can return to it. It stores conversation IDs
// and paths only: whether the signed-in account may open a conversation is
// for the server to decide.
package resume

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"

	"github.com/datadog-labs/bits-cli/internal/config"
)

const (
	fileName = "conversations.json"
	lockName = "conversations.lock"

	// maxConversations bounds the file; the least recently opened go first.
	maxConversations = 500

	lockRetry = 25 * time.Millisecond
)

// errCorrupt marks a file that exists but does not decode.
var errCorrupt = errors.New("conversation history is unreadable")

type record struct {
	Workspace string    `json:"workspace"`
	OpenedAt  time.Time `json:"opened_at"`
}

type state struct {
	Conversations map[string]record `json:"conversations"`
}

// History is the conversations opened in one workspace. Several processes may
// share it: each record is one conversation, and the most recent opening wins.
type History struct {
	dir       string
	workspace string
}

// Open returns the history kept for workspace, the directory a run belongs to
// (see workspace.Workspace.Root).
func Open(workspace string) (History, error) {
	dir, err := config.Dir()
	if err != nil {
		return History{}, err
	}
	return History{dir: dir, workspace: workspace}, nil
}

// Workspace is the directory the history is kept for.
func (h History) Workspace() string { return h.workspace }

// Recent returns the conversations opened in the workspace, most recent first.
func (h History) Recent() ([]string, error) {
	s, err := h.load()
	if err != nil {
		return nil, err
	}
	type opened struct {
		id string
		at time.Time
	}
	var found []opened
	for id, r := range s.Conversations {
		if r.Workspace == h.workspace {
			found = append(found, opened{id, r.OpenedAt})
		}
	}
	slices.SortFunc(found, func(a, b opened) int {
		return cmp.Or(b.at.Compare(a.at), cmp.Compare(a.id, b.id))
	})
	ids := make([]string, len(found))
	for i, o := range found {
		ids[i] = o.id
	}
	return ids, nil
}

// Record notes that conversation id was open in the workspace at time at. It
// moves the conversation here from any other workspace, unless that record is
// newer, as when a concurrent process wrote first.
func (h History) Record(ctx context.Context, id string, at time.Time) error {
	return h.locked(ctx, func() error {
		s, err := h.load()
		if errors.Is(err, errCorrupt) {
			// The file only saves the user a lookup: replace it rather than
			// leave --last failing until someone deletes it by hand.
			// NOTE: a file a newer Bits wrote in a shape this one cannot
			// decode is replaced the same way.
			s, err = state{}, nil
		}
		if err != nil {
			return err
		}
		if previous, ok := s.Conversations[id]; ok && previous.OpenedAt.After(at) {
			return nil
		}
		if s.Conversations == nil {
			s.Conversations = map[string]record{}
		}
		s.Conversations[id] = record{Workspace: h.workspace, OpenedAt: at.UTC()}
		prune(s.Conversations)
		return h.save(s)
	})
}

func prune(conversations map[string]record) {
	excess := len(conversations) - maxConversations
	if excess <= 0 {
		return
	}
	ids := make([]string, 0, len(conversations))
	for id := range conversations {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		return cmp.Or(conversations[a].OpenedAt.Compare(conversations[b].OpenedAt), cmp.Compare(a, b))
	})
	for _, id := range ids[:excess] {
		delete(conversations, id)
	}
}

func (h History) path() string { return filepath.Join(h.dir, fileName) }

// load reads without the lock: save replaces the file atomically, so a reader
// sees either the old or the new state.
func (h History) load() (state, error) {
	raw, err := os.ReadFile(h.path())
	if errors.Is(err, fs.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, fmt.Errorf("read conversation history: %w", err)
	}
	var s state
	if err := json.Unmarshal(raw, &s); err != nil {
		return state{}, fmt.Errorf("%w: %s: %w", errCorrupt, h.path(), err)
	}
	return s, nil
}

func (h History) save(s state) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(h.dir, ".conversations-*.tmp")
	if err != nil {
		return fmt.Errorf("save conversation history: %w", err)
	}
	_, err = f.Write(raw)
	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Rename(f.Name(), h.path())
	}
	if err != nil {
		return fmt.Errorf("save conversation history: %w", errors.Join(err, os.Remove(f.Name())))
	}
	return nil
}

// locked runs fn holding the cross-process lock, so concurrent records do not
// overwrite each other's read-modify-write.
func (h History) locked(ctx context.Context, fn func() error) (err error) {
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	lock := flock.New(filepath.Join(h.dir, lockName))
	ok, err := lock.TryLockContext(ctx, lockRetry)
	if err == nil && !ok {
		err = ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("lock conversation history: %w", err)
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("unlock conversation history: %w", unlockErr))
		}
	}()
	return fn()
}

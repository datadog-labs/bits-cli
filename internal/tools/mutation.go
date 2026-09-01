package tools

import (
	"context"
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"strconv"
)

// mutationLocker serializes every workspace mutation. A single lock (rather
// than one per path) is intentional: two lexically different paths can resolve
// to the same file through a symlinked parent directory, a hardlink, or a
// case-insensitive filesystem, and a per-path lock would let such aliases run
// concurrently and lose an update. Serializing all mutations sidesteps the
// need to resolve file identity; mutations are infrequent and quick, so the
// lost cross-file parallelism is negligible.
//
// The lock is a 1-slot channel rather than a sync.Mutex so a waiter can abort
// on context cancellation instead of blocking behind a slow mutation.
type mutationLocker struct {
	ch chan struct{}
}

func newMutationLocker() *mutationLocker {
	return &mutationLocker{ch: make(chan struct{}, 1)}
}

// lock acquires the mutation lock, returning its unlock function. It returns
// ctx.Err() instead if the context is cancelled while waiting; the unlock
// function is nil in that case.
func (l *mutationLocker) lock(ctx context.Context) (func(), error) {
	select {
	case l.ch <- struct{}{}:
		return func() { <-l.ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

const tmpFilePrefix = ".bits-tmp."

// safeReplace writes data to relPath under r, creating missing parent
// directories, via a temporary file in the target directory that is renamed
// into place. A failed or interrupted write therefore never leaves a partially
// written or truncated target: the original file is replaced only once the new
// content is fully on disk. When the target already exists as a regular file,
// its permission bits are preserved.
//
// ctx is checked immediately before the rename, the sole irreversible step, so
// a cancellation observed after the caller's gate does not commit the write.
//
// A new file honors the process umask (like a normal create); overwriting an
// existing regular file preserves its exact permission bits, since umask
// governs creation, not edits.
func safeReplace(ctx context.Context, r *os.Root, relPath string, data []byte) error {
	dir := path.Dir(relPath)
	if dir != "." {
		if err := r.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	perm := os.FileMode(0o644)
	preserve := false
	if info, err := r.Lstat(relPath); err == nil && info.Mode().IsRegular() {
		perm = info.Mode().Perm()
		preserve = true
	}

	// Create at perm so O_CREATE applies umask: a new file lands at
	// perm&^umask, and an overwrite's temp is never more permissive than the
	// target it replaces.
	f, tmp, err := createTemp(r, dir, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = r.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	// Restore the existing file's exact bits, which umask may have masked off
	// the temp. New files keep their umask-adjusted create mode.
	if preserve {
		if err := r.Chmod(tmp, perm); err != nil {
			_ = r.Remove(tmp)
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	if err := r.Rename(tmp, relPath); err != nil {
		_ = r.Remove(tmp)
		return err
	}
	return nil
}

// createTemp opens a new scratch file in dir under the workspace root. It
// mirrors os.CreateTemp: a random name opened with O_EXCL, retried on
// collision, so it never adopts or truncates a pre-existing file. os.CreateTemp
// itself cannot be used because it operates on absolute paths and would bypass
// the root's confinement.
func createTemp(r *os.Root, dir string, perm os.FileMode) (*os.File, string, error) {
	for try := 0; ; try++ {
		// nextRandom, as in os.CreateTemp: a 32-bit random formatted base 10.
		name := path.Join(dir, tmpFilePrefix+strconv.FormatUint(uint64(rand.Uint32()), 10))
		f, err := r.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, fs.ErrExist) {
			if try < 10000 {
				continue
			}
			return nil, "", errors.New("createtemp: exhausted attempts to find an unused name")
		}
		if err != nil {
			return nil, "", err
		}
		return f, name, nil
	}
}

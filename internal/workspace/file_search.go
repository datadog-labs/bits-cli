package workspace

import (
	"context"
	"io/fs"
	"sync"
	"time"

	"github.com/sahilm/fuzzy"
)

const (
	fileSearchUpdateInterval  = 250 * time.Millisecond
	fileSearchCandidateBuffer = 256
)

const (
	// DefaultFileSearchLimit is used when FileSearchOptions.Limit is not positive.
	DefaultFileSearchLimit = 10
	// MaximumFileSearchLimit bounds snapshots even when a larger limit is requested.
	MaximumFileSearchLimit = 100
)

// FileSearchOptions configures a file-search session.
type FileSearchOptions struct {
	// Limit is the maximum number of results in a snapshot. Values below one
	// use a default, and values above the package maximum are capped.
	Limit int
}

// FileSearchResult describes one regular file matched within the workspace.
type FileSearchResult struct {
	Path  string
	Score int
	// MatchedIndexes contains UTF-8 byte offsets into Path.
	MatchedIndexes []int
}

// FileSearchSnapshot is an immutable view of the indexed files matching Query.
// Generation is returned by UpdateQuery, allowing consumers to reject stale
// snapshots after a newer query has been submitted.
type FileSearchSnapshot struct {
	Query          string
	Generation     uint64
	Results        []FileSearchResult
	CandidateCount int
	Complete       bool
	Err            error
}

type fileSearchQuery struct {
	value      string
	generation uint64
}

type fileSearchWalk func(context.Context, WalkFunc) error

// FileSearchSession indexes a workspace once and ranks new queries against the
// in-memory candidates. Close the session before closing its Workspace.
type FileSearchSession struct {
	cancel context.CancelFunc
	done   chan struct{}

	queryMu   sync.Mutex
	query     fileSearchQuery
	closed    bool
	queryWake chan struct{}
	updates   chan FileSearchSnapshot
	closeOnce sync.Once
}

// NewFileSearchSession starts one asynchronous walk of the workspace.
func (w *Workspace) NewFileSearchSession(ctx context.Context, options FileSearchOptions) *FileSearchSession {
	return newFileSearchSession(ctx, func(ctx context.Context, visit WalkFunc) error {
		return w.Walk(ctx, ".", visit)
	}, options)
}

func newFileSearchSession(ctx context.Context, walk fileSearchWalk, options FileSearchOptions) *FileSearchSession {
	ctx, cancel := context.WithCancel(ctx)
	session := &FileSearchSession{
		cancel:    cancel,
		done:      make(chan struct{}),
		queryWake: make(chan struct{}, 1),
		updates:   make(chan FileSearchSnapshot, 1),
	}
	go session.run(ctx, walk, fileSearchLimit(options.Limit))
	return session
}

// UpdateQuery replaces the active query without waiting for the walk or fuzzy
// matching. It returns the query generation stamped onto resulting snapshots.
// Calls made after Close are no-ops and return the last generation.
func (s *FileSearchSession) UpdateQuery(query string) uint64 {
	s.queryMu.Lock()
	if s.closed {
		generation := s.query.generation
		s.queryMu.Unlock()
		return generation
	}
	s.query.generation++
	s.query.value = query
	generation := s.query.generation
	s.queryMu.Unlock()

	select {
	case s.queryWake <- struct{}{}:
	default:
	}
	return generation
}

// Updates returns coalesced search snapshots. The channel closes when the
// session's context is canceled or Close returns. Consumers should compare a
// snapshot's Generation with the latest value returned by UpdateQuery.
func (s *FileSearchSession) Updates() <-chan FileSearchSnapshot {
	return s.updates
}

// Close cancels the walk and waits for every session goroutine to finish. It is
// safe to call Close more than once.
func (s *FileSearchSession) Close() {
	s.closeOnce.Do(func() {
		s.queryMu.Lock()
		s.closed = true
		s.queryMu.Unlock()
		s.cancel()
	})
	<-s.done
}

func (s *FileSearchSession) run(ctx context.Context, walk fileSearchWalk, limit int) {
	candidates := make(chan string, fileSearchCandidateBuffer)
	walkResult := make(chan error, 1)
	var walkers sync.WaitGroup
	walkers.Add(1)
	go func() {
		defer walkers.Done()
		err := walk(ctx, func(filePath string, entry fs.DirEntry) error {
			if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			select {
			case candidates <- filePath:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		walkResult <- err
		close(candidates)
	}()
	defer func() {
		walkers.Wait()
		s.queryMu.Lock()
		s.closed = true
		s.queryMu.Unlock()
		close(s.updates)
		close(s.done)
	}()

	partialTimer := time.NewTimer(fileSearchUpdateInterval)
	defer partialTimer.Stop()
	partialTimerC := partialTimer.C

	var query fileSearchQuery
	paths := make([]string, 0, fileSearchCandidateBuffer)
	dirty := false
	complete := false
	var walkErr error

	for {
		select {
		case <-ctx.Done():
			return

		case <-s.queryWake:
			query = s.currentQuery()
			s.publish(fileSearchSnapshot(query, paths, limit, complete, walkErr))
			if !complete {
				partialTimer.Reset(fileSearchUpdateInterval)
			}

		case filePath, ok := <-candidates:
			if !ok {
				candidates = nil
				walkErr = <-walkResult
				if ctx.Err() != nil {
					return
				}
				complete = true
				dirty = false
				partialTimer.Stop()
				partialTimerC = nil
				query = s.currentQuery()
				s.publish(fileSearchSnapshot(query, paths, limit, true, walkErr))
				continue
			}
			paths = append(paths, filePath)
			dirty = true

		case <-partialTimerC:
			if dirty {
				dirty = false
				query = s.currentQuery()
				s.publish(fileSearchSnapshot(query, paths, limit, complete, walkErr))
			}
			partialTimer.Reset(fileSearchUpdateInterval)
		}
	}
}

func (s *FileSearchSession) currentQuery() fileSearchQuery {
	s.queryMu.Lock()
	defer s.queryMu.Unlock()
	return s.query
}

// publish retains only the newest snapshot when a consumer is slower than the
// indexer. This prevents an unread updates channel from blocking Close.
func (s *FileSearchSession) publish(snapshot FileSearchSnapshot) {
	select {
	case s.updates <- snapshot:
		return
	default:
	}
	select {
	case <-s.updates:
	default:
	}
	select {
	case s.updates <- snapshot:
	default:
	}
}

func fileSearchSnapshot(query fileSearchQuery, paths []string, limit int, complete bool, err error) FileSearchSnapshot {
	return FileSearchSnapshot{
		Query:          query.value,
		Generation:     query.generation,
		Results:        rankFileSearch(query.value, paths, limit),
		CandidateCount: len(paths),
		Complete:       complete,
		Err:            err,
	}
}

func rankFileSearch(query string, paths []string, limit int) []FileSearchResult {
	matches := fuzzy.Find(query, paths)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	results := make([]FileSearchResult, len(matches))
	for i, match := range matches {
		results[i] = FileSearchResult{
			Path:           match.Str,
			Score:          match.Score,
			MatchedIndexes: append([]int(nil), match.MatchedIndexes...),
		}
	}
	return results
}

func fileSearchLimit(limit int) int {
	if limit < 1 {
		return DefaultFileSearchLimit
	}
	if limit > MaximumFileSearchLimit {
		return MaximumFileSearchLimit
	}
	return limit
}

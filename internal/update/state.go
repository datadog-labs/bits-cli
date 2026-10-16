package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/datadog-labs/bits-cli/internal/version"
)

type state struct {
	Backend         string    `json:"backend"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	CheckedAt       time.Time `json:"checked_at,omitempty"`
	ExcludedVersion string    `json:"excluded_version,omitempty"`
	PostponedUntil  time.Time `json:"postponed_until,omitempty"`
}

func (s state) stale(now time.Time) bool {
	return !version.IsStable(s.LatestVersion) || s.CheckedAt.IsZero() ||
		s.CheckedAt.After(now) || now.Sub(s.CheckedAt) >= ttl
}

// postponed ignores a postponement longer than the one postpone sets, which
// only a clock moved backwards produces, as stale does for CheckedAt.
func (s state) postponed(now time.Time) bool {
	remaining := s.PostponedUntil.Sub(now)
	return remaining > 0 && remaining <= postponement
}

func (c *Checker) load() state {
	var s state
	raw, err := os.ReadFile(c.path)
	if err != nil || json.Unmarshal(raw, &s) != nil || s.Backend != c.backend.Name() {
		return state{Backend: c.backend.Name()}
	}
	return s
}

// exclude persists the version to skip: check stops reporting it until a newer
// release replaces it. An empty version clears the exclusion.
func (c *Checker) exclude(excluded string) error {
	if c.backend == nil {
		return ErrUnsupportedInstallation
	}
	if excluded != "" && !version.IsStable(excluded) {
		return fmt.Errorf("%w: excluded version %q", errInvalidVersion, excluded)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.load()
	s.ExcludedVersion = excluded
	return c.save(s)
}

// postpone stops check from reporting any version for a day.
func (c *Checker) postpone() error {
	if c.backend == nil {
		return ErrUnsupportedInstallation
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.load()
	s.PostponedUntil = time.Now().UTC().Add(postponement)
	return c.save(s)
}

// Replace state atomically so readers never see a partial file. Callers that
// modify loaded state hold c.mu across the read and write. Separate Bits
// processes have separate checkers, so concurrent writes across processes
// remain last write wins.
func (c *Checker) save(s state) error {
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".update-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.path)
}

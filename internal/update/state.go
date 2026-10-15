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
}

func (s state) stale(now time.Time) bool {
	return !version.IsStable(s.LatestVersion) || s.CheckedAt.IsZero() ||
		s.CheckedAt.After(now) || now.Sub(s.CheckedAt) >= ttl
}

func (c *Checker) load() state {
	var s state
	raw, err := os.ReadFile(c.path)
	if err != nil || json.Unmarshal(raw, &s) != nil || s.Backend != c.backend.Name() {
		return state{Backend: c.backend.Name()}
	}
	return s
}

// Exclude persists the version to skip. An empty version clears the exclusion.
// This is mechanism only; no command or UI exposes it yet.
func (c *Checker) Exclude(excluded string) error {
	if excluded != "" && !version.IsStable(excluded) {
		return fmt.Errorf("%w: excluded version %q", ErrInvalidVersion, excluded)
	}
	s := c.load()
	s.ExcludedVersion = excluded
	return c.save(s)
}

// Replace state atomically so readers never see a partial file. Concurrent
// writers are best-effort: the last write wins, including exclusions.
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

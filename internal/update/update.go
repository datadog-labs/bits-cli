// Package update checks cached release information and refreshes it in the
// background. It never installs an update or writes terminal output.
package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/datadog-labs/bits-cli/internal/config"
	"github.com/datadog-labs/bits-cli/internal/version"
	"golang.org/x/mod/semver"
)

const ttl = 24 * time.Hour

var (
	// ErrUnsupportedInstallation means no supported installation backend was found.
	ErrUnsupportedInstallation = errors.New("unsupported installation for update checks")
	// ErrInvalidVersion means version information is missing, ambiguous, or unsupported.
	ErrInvalidVersion = errors.New("invalid update version")
)

type backend interface {
	Name() string
	LatestVersion(context.Context) (string, error)
}

// Checker owns update state for the detected installation backend.
type Checker struct {
	backend backend
	path    string
	once    sync.Once
}

// New returns an error for unsupported installations or local discovery failures.
// Callers can silently disable checking for this launch when it fails.
func New() (*Checker, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	b, err := detectBackend(executable)
	if err != nil {
		return nil, err
	}
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return &Checker{
		backend: b,
		path:    filepath.Join(dir, "update.json"),
	}, nil
}

// Check returns the cached newer version, or an empty string. It starts at most
// one background refresh per Checker when state is missing or expired. The
// result of that refresh is only used on a later call or launch. No network work
// is awaited, including on shutdown; cancellation stops the best-effort refresh.
func (c *Checker) Check(ctx context.Context, current string) string {
	if !version.IsStable(current) {
		return ""
	}
	s := c.load()
	if s.stale(time.Now()) {
		c.once.Do(func() { go func() { _ = c.refresh(ctx) }() })
	}
	if version.IsStable(s.LatestVersion) && semver.Compare(s.LatestVersion, current) > 0 && s.LatestVersion != s.ExcludedVersion {
		return s.LatestVersion
	}
	return ""
}

func (c *Checker) refresh(ctx context.Context) error {
	latest, err := c.backend.LatestVersion(ctx)
	if err != nil {
		return fmt.Errorf("check %s update: %w", c.backend.Name(), err)
	}
	if !version.IsStable(latest) {
		return fmt.Errorf("%w: %s returned %q", ErrInvalidVersion, c.backend.Name(), latest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s := c.load()
	s.LatestVersion = latest
	s.CheckedAt = time.Now().UTC()
	return c.save(s)
}

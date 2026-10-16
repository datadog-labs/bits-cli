// Package update checks cached release information and refreshes it in the
// background. It installs an update only through Upgrade, on explicit request.
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/datadog-labs/bits-cli/internal/version"
	"golang.org/x/mod/semver"
)

const (
	ttl          = 24 * time.Hour
	postponement = 24 * time.Hour
)

var (
	// ErrUnsupportedInstallation means no supported installation backend was found.
	ErrUnsupportedInstallation = errors.New("automatic updates are unavailable for this installation")
	// errInvalidVersion means version information is missing, ambiguous, or unsupported.
	errInvalidVersion = errors.New("invalid update version")
)

type backend interface {
	Name() string
	LatestVersion(context.Context) (string, error)
	Command() string
	Upgrade(stdin io.Reader, stdout, stderr io.Writer) error
}

// Checker owns update state for the detected installation backend.
type Checker struct {
	backend backend
	path    string
	once    sync.Once
	mu      sync.Mutex
}

// Offer describes the update available at startup and the user's choices.
// Latest is empty when there is no newer release to offer.
type Offer struct {
	Current, Latest string
	Postpone, Skip  func() error
}

// New always returns a checker keeping its state under the config directory
// dir. It is unavailable when the binary was not installed by a supported
// backend (a development build, go install), or the executable cannot be
// located.
func New(dir string) *Checker {
	c := &Checker{}
	executable, err := os.Executable()
	if err != nil {
		return c
	}
	b, err := detectBackend(executable)
	if err != nil {
		return c
	}
	c.backend = b
	c.path = filepath.Join(dir, "update.json")
	return c
}

// Offer returns nil for an unsupported installation. With check enabled, it
// includes the cached newer release, if any, for the startup prompt.
func (c *Checker) Offer(ctx context.Context, check bool) *Offer {
	if c.backend == nil {
		return nil
	}
	offer := &Offer{Current: version.Build(), Postpone: c.postpone}
	if check {
		offer.Latest = c.check(ctx, offer.Current)
	}
	offer.Skip = func() error { return c.exclude(offer.Latest) }
	return offer
}

// check returns the cached newer version unless it is excluded or postponed,
// or an empty string. It starts at most one background refresh per Checker
// when state is missing or expired. The result of that refresh is only used on
// a later call or launch. No network work is awaited, including on shutdown;
// cancellation stops the best-effort refresh.
func (c *Checker) check(ctx context.Context, current string) string {
	if c.backend == nil || !version.IsStable(current) {
		return ""
	}
	s, now := c.load(), time.Now()
	if s.stale(now) {
		c.once.Do(func() { go func() { _ = c.refresh(ctx) }() })
	}
	if version.IsStable(s.LatestVersion) && semver.Compare(s.LatestVersion, current) > 0 &&
		s.LatestVersion != s.ExcludedVersion && !s.postponed(now) {
		return s.LatestVersion
	}
	return ""
}

// Upgrade installs the latest release in the foreground with the installer
// attached to the given streams, so call it only once the TUI has released
// the terminal. The running process keeps the old binary; the new version
// starts on the next launch. It takes no context: the installer must finish
// or clean up on its own, since killing it mid-install can break the keg.
// Every attempt postpones the offer, like postpone: a failed or declined one,
// and one where brew exits 0 without installing (a pinned formula, auto-update
// disabled). After a real upgrade the postponement has no effect. Brew's output
// has the last word because an exit code of 0 does not guarantee an install.
func (c *Checker) Upgrade(stdin io.Reader, stdout, stderr io.Writer) error {
	if c.backend == nil {
		return ErrUnsupportedInstallation
	}
	_, _ = fmt.Fprintf(stderr, "Updating Bits with %s\n", c.backend.Command())
	err := c.backend.Upgrade(stdin, stdout, stderr)
	if err = errors.Join(err, c.postpone()); err != nil {
		return fmt.Errorf("update Bits: %w", err)
	}
	return nil
}

func (c *Checker) refresh(ctx context.Context) error {
	latest, err := c.backend.LatestVersion(ctx)
	if err != nil {
		return fmt.Errorf("check %s update: %w", c.backend.Name(), err)
	}
	if !version.IsStable(latest) {
		return fmt.Errorf("%w: %s returned %q", errInvalidVersion, c.backend.Name(), latest)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.load()
	s.LatestVersion = latest
	s.CheckedAt = time.Now().UTC()
	return c.save(s)
}

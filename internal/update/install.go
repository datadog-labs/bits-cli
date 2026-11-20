package update

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/datadog-labs/bits-cli/internal/version"
	"golang.org/x/mod/semver"
)

// A standalone installation keeps each release archive unpacked in its own
// directory and points current at the active one:
//
//	<root>/versions/<v>/      bin/, libexec/, share/, .install-complete
//	<root>/current            -> versions/<v>
//	~/.local/bin/bits         -> <root>/current/bin/bits  (Windows: <root>\current\bin on PATH)
//
// The install scripts and Upgrade unpack a release into versions/<v>, mark it
// complete, and run its bits install, so the version being installed owns the
// switch. Released binaries and scripts rely on these names: never change them.
const (
	versionsDir    = "versions"
	currentLink    = "current"
	completeMarker = ".install-complete"
	stagingPrefix  = ".staging-"
)

// A staging directory this old belongs to an install that died.
const stagingExpiry = 24 * time.Hour

// errNotStandalone means the executable is not in a standalone installation.
var errNotStandalone = errors.New("not a standalone installation")

// InstallOptions is what bits install needs from its environment.
type InstallOptions struct {
	// Executable is the running bits, in <root>/versions/<v>/bin.
	Executable string
	// Home holds ~/.local/bin; Windows uses PATH instead.
	Home string
	// Shell is $SHELL, which picks the PATH advice.
	Shell string
	// Path is $PATH, to tell which bits runs first.
	Path string
	// UserEnvironment is the HKCU key whose Path Windows installs add to:
	// Environment, except in tests.
	UserEnvironment string
}

// Install makes the release Executable belongs to the active one, removes
// older versions but the one it replaces, and sets up the bits
// command on first use. A cleanup failure is reported to out, not returned:
// the install itself succeeded.
func Install(opts InstallOptions, out io.Writer) error {
	root, v, err := standaloneVersion(opts.Executable)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, versionsDir, v)
	if _, err := os.Stat(filepath.Join(dir, completeMarker)); err != nil {
		return fmt.Errorf("%s is not a complete installation: %w", dir, err)
	}
	previous, err := activeVersion(root)
	if err != nil {
		return err
	}
	if err := activate(root, v); err != nil {
		return fmt.Errorf("activate Bits %s: %w", v, err)
	}
	for _, err := range cleanUp(root, v, previous) {
		_, _ = fmt.Fprintf(out, "Warning: %v\n", err)
	}
	_, _ = fmt.Fprintf(out, "Installed Bits %s in %s\n", v, root)
	return setUpCommand(root, opts, out)
}

// standaloneVersion returns the root and version of the installation the
// executable was unpacked into.
func standaloneVersion(executable string) (root, v string, err error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "", fmt.Errorf("resolve executable: %w", err)
	}
	bin := filepath.Dir(resolved)
	dir := filepath.Dir(bin)
	versions := filepath.Dir(dir)
	v = filepath.Base(dir)
	if filepath.Base(resolved) != executableName || filepath.Base(bin) != "bin" ||
		filepath.Base(versions) != versionsDir || !version.IsStable(v) {
		return "", "", fmt.Errorf("%w: %s", errNotStandalone, resolved)
	}
	return filepath.Dir(versions), v, nil
}

// activeVersion returns the version current points at, or "" before the
// first install.
func activeVersion(root string) (string, error) {
	current := filepath.Join(root, currentLink)
	info, err := os.Lstat(current)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// On Windows, a first activation creates current as a plain directory
	// before making it a junction, so a directory is one that another install
	// has yet to finish, or that stopped there. Lstat reports neither a
	// junction nor a symlink as a directory, and current only ever turns from
	// the first into the second, so checking first leaves no race.
	if info.IsDir() {
		return "", nil
	}
	target, err := os.Readlink(current)
	if err != nil {
		return "", err
	}
	return filepath.Base(target), nil
}

// cleanUp removes the versions older than installed except previous, and
// staging directories left by installs that died. A newer version may belong
// to an upgrade running concurrently, so it stays. A reinstall, where previous
// is installed, cannot tell which version to roll back to, so it removes no
// version. Entries the installer does not create stay too.
// NOTE: an older version installed concurrently can still lose its directory
// after a newer install read current; installing again repairs it.
// NOTE: a session left running across two upgrades loses its own release,
// so its next helper launch fails until it restarts, as with brew cleanup.
func cleanUp(root, installed, previous string) []error {
	var errs []error
	versions, err := os.ReadDir(filepath.Join(root, versionsDir))
	if err != nil {
		errs = append(errs, err)
	}
	for _, entry := range versions {
		name := entry.Name()
		if previous == installed || !entry.IsDir() || !version.IsStable(name) || name == previous || semver.Compare(name, installed) >= 0 {
			continue
		}
		if err := removeVersion(root, name); err != nil {
			errs = append(errs, fmt.Errorf("remove Bits %s: %w", name, err))
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return append(errs, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if time.Since(info.ModTime()) < stagingExpiry {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// removeVersion moves versions/<v> into a staging directory before deleting
// it, so versions only ever holds complete releases. On Windows, a running
// bits.exe makes either the move fail, leaving the version whole, or the
// delete, leaving a staging directory that a later install sweeps once it has
// exited, as it does after a Ctrl-C.
func removeVersion(root, v string) error {
	trash, err := os.MkdirTemp(root, stagingPrefix+"*")
	if err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(root, versionsDir, v), filepath.Join(trash, v)); err != nil {
		return errors.Join(err, os.Remove(trash))
	}
	return os.RemoveAll(trash)
}

// firstOnPath returns the first file named name in a PATH list, as the
// shell would run it, or "".
func firstOnPath(path, name string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

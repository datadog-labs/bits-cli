//go:build !windows

package update

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const executableName = "bits"

// resolveExecutable follows ~/.local/bin/bits and current to the version
// that runs.
func resolveExecutable(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// activate points current at versions/<v>. The link is relative, so the
// root can move, and renamed over the old one, so bits never goes missing.
func activate(root, v string) error {
	return replaceSymlink(filepath.Join(versionsDir, v), filepath.Join(root, currentLink))
}

// replaceSymlink atomically points link at target. The temporary name is per
// process, so concurrent installs do not collide.
func replaceSymlink(target, link string) error {
	tmp := fmt.Sprintf("%s.%d.tmp", link, os.Getpid())
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		return errors.Join(err, os.Remove(tmp))
	}
	return nil
}

// setUpCommand links ~/.local/bin/bits to the active version and says how to
// put it on PATH.
func setUpCommand(root string, opts InstallOptions, out io.Writer) error {
	bin := filepath.Join(opts.Home, ".local", "bin")
	link := filepath.Join(bin, executableName)
	target := filepath.Join(root, currentLink, "bin", executableName)
	existing, err := os.Readlink(link)
	created := errors.Is(err, fs.ErrNotExist)
	switch {
	case created:
		if err := os.MkdirAll(bin, 0o755); err != nil {
			return err
		}
		if err := os.Symlink(target, link); err != nil {
			// A concurrent install may have created it first.
			existing, readErr := os.Readlink(link)
			if readErr != nil || !isCommandLink(existing) {
				return err
			}
		}
	case err == nil && isCommandLink(existing):
		if existing != target {
			if err := replaceSymlink(target, link); err != nil {
				return err
			}
			created = true
		}
	default:
		return fmt.Errorf("%s already exists and the installer did not create it; remove it and install again", link)
	}
	_, err = io.WriteString(out, pathAdvice(bin, link, created, opts))
	return err
}

// isCommandLink reports whether target is the current bits of some
// installation, so a moved root takes over the link but another bits never
// gets replaced.
func isCommandLink(target string) bool {
	return strings.HasSuffix(target, string(filepath.Separator)+filepath.Join("bits", currentLink, "bin", executableName))
}

// pathAdvice stays quiet on an upgrade that finds everything in place.
func pathAdvice(bin, link string, created bool, opts InstallOptions) string {
	switch first := firstOnPath(opts.Path, executableName); first {
	case link:
		if !created {
			return ""
		}
		return "Run bits to get started.\n"
	case "":
		if command := pathCommand(filepath.Base(opts.Shell)); command != "" {
			return fmt.Sprintf("%s is not on your PATH. Add it with:\n\n  %s\n\nThen open a new terminal and run bits.\n", bin, command)
		}
		return fmt.Sprintf("Add %s to your PATH, then open a new terminal and run bits.\n", bin)
	default:
		return fmt.Sprintf("%s comes first on your PATH, so run %s to use this one.\n", first, link)
	}
}

// pathCommand puts ~/.local/bin first on PATH for new shells. zsh reads its
// rc from ZDOTDIR when set, and macOS terminals start bash as a login shell,
// which reads ~/.bash_profile but not ~/.bashrc.
func pathCommand(shell string) string {
	const export = `echo 'export PATH="$HOME/.local/bin:$PATH"' >> `
	switch shell {
	case "zsh":
		return export + `"${ZDOTDIR:-$HOME}/.zshrc"`
	case "bash":
		if runtime.GOOS == "darwin" {
			return export + "~/.bash_profile"
		}
		return export + "~/.bashrc"
	case "fish":
		return "fish_add_path ~/.local/bin"
	}
	return ""
}

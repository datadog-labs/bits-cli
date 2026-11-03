// Package libexec locates the helper programs installed with bits, in
// libexec/bits/<name> of its install prefix.
package libexec

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// DirEnv overrides the libexec/bits directory, for development.
const DirEnv = "BITS_LIBEXEC_DIR"

var executable = os.Executable

func helperDir(name string) (string, error) {
	root := os.Getenv(DirEnv)
	if root == "" {
		exe, err := executable()
		if err != nil {
			return "", err
		}
		// Homebrew links bin/bits from outside the prefix.
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return "", err
		}
		root = filepath.Join(filepath.Dir(exe), "..", "libexec", "bits")
	}
	dir, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	return dir, nil
}

// Binary resolves a helper's executable without probing the filesystem. Start
// it with exec.CommandContext; exec is authoritative about whether it can run.
func Binary(name string) (string, error) {
	dir, err := helperDir(name)
	if err != nil {
		return "", err
	}
	binary := name
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	return filepath.Join(dir, binary), nil
}

// Dir returns the existing helper directory, for callers that need its assets.
func Dir(name string) (string, error) {
	dir, err := helperDir(name)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("locate %s helper: %w", name, err)
	} else if !info.IsDir() {
		return "", fmt.Errorf("%s helper path is not a directory: %s", name, dir)
	}
	return dir, nil
}

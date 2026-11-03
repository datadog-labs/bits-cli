// Package libexec locates the helper programs installed with bits, in
// libexec/bits/<name> of its install prefix.
package libexec

import (
	"fmt"
	"os"
	"path/filepath"
)

// DirEnv overrides the libexec/bits directory, for development.
const DirEnv = "BITS_LIBEXEC_DIR"

var executable = os.Executable

// Dir returns the directory of the named helper.
func Dir(name string) (string, error) {
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
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not installed in %s; reinstall bits or set %s", name, root, DirEnv)
	}
	return dir, nil
}

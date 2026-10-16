// Package config owns the location of Bits CLI's per-user configuration and state.
package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// Dir returns the Bits CLI config directory without creating it. An explicit
// BITS_CONFIG_DIR overrides the default under the OS account's home; HOME and
// XDG_CONFIG_HOME do not affect the default. Callers own their filenames and
// any directory creation or permission checks.
func Dir() (string, error) {
	if dir := os.Getenv("BITS_CONFIG_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("locate Bits CLI config directory: BITS_CONFIG_DIR must be absolute")
		}
		return filepath.Clean(dir), nil
	}
	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("locate Bits CLI config directory: %w", err)
	}
	if currentUser.HomeDir == "" {
		return "", fmt.Errorf("locate Bits CLI config directory: home directory is empty")
	}
	return filepath.Join(currentUser.HomeDir, ".bits-cli"), nil
}

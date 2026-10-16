// Package config owns the location of Bits CLI's per-user configuration and state.
package config

import (
	"fmt"
	"os/user"
	"path/filepath"
)

// Dir returns the user's .bits-cli directory without creating it. Use the OS
// account's home, independent of HOME and XDG_CONFIG_HOME, so processes share
// the same credential files and locks even when their environments differ.
// Callers own their filenames and any directory creation or permission checks.
func Dir() (string, error) {
	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("locate Bits CLI config directory: %w", err)
	}
	if currentUser.HomeDir == "" {
		return "", fmt.Errorf("locate Bits CLI config directory: home directory is empty")
	}
	return filepath.Join(currentUser.HomeDir, ".bits-cli"), nil
}

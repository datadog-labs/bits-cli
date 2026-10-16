package config

import (
	"fmt"
	"os/user"
	"path/filepath"
)

// loadDir locates the Bits CLI config directory without creating it. An
// explicit BITS_CONFIG_DIR overrides the default under the OS account's home;
// HOME and XDG_CONFIG_HOME do not affect the default. Callers own their
// filenames and any directory creation or permission checks.
func loadDir() (string, error) {
	if dir := getEnv(EnvConfigDir); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("locate Bits CLI config directory: %s must be absolute", EnvConfigDir)
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

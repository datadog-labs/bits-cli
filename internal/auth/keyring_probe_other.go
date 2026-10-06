//go:build !linux

package auth

// KeyringAvailable reports whether the OS credential manager is reachable. The
// macOS Keychain and Windows Credential Manager are always present, so this is
// unconditionally true and never triggers the file fallback.
func KeyringAvailable() bool {
	return true
}

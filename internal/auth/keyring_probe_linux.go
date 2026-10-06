//go:build linux

package auth

import (
	dbus "github.com/godbus/dbus/v5"
)

const secretServiceName = "org.freedesktop.secrets"

// KeyringAvailable reports whether a Secret Service provider is present on the
// D-Bus session bus. It checks name availability rather than reading a secret,
// so it never unlocks the keyring or prompts the user: a present-but-locked
// keyring is reported available and unlocks lazily on the first real access.
func KeyringAvailable() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()

	bus := conn.BusObject()
	var hasOwner bool
	if err := bus.Call("org.freedesktop.DBus.NameHasOwner", 0, secretServiceName).Store(&hasOwner); err == nil && hasOwner {
		return true
	}

	// Not currently running, but a .service file means it activates on demand.
	var activatable []string
	if err := bus.Call("org.freedesktop.DBus.ListActivatableNames", 0).Store(&activatable); err != nil {
		return false
	}
	for _, name := range activatable {
		if name == secretServiceName {
			return true
		}
	}
	return false
}

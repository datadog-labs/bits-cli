package scripts

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// keepUserPath restores the user PATH that bits install changes.
func keepUserPath(t *testing.T) {
	t.Helper()
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	path, kind, err := key.GetStringValue("Path")
	missing := errors.Is(err, registry.ErrNotExist)
	if err != nil && !missing {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer func() { _ = key.Close() }()
		switch {
		case missing:
			err = key.DeleteValue("Path")
		case kind == registry.EXPAND_SZ:
			err = key.SetExpandStringValue("Path", path)
		default:
			err = key.SetStringValue("Path", path)
		}
		if err != nil {
			t.Errorf("restore the user PATH: %v", err)
		}
	})
}

package update

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// isolatedOptions restores the user PATH that Install changes, which no
// test can redirect.
func isolatedOptions(t *testing.T) InstallOptions {
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
		var err error
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
	return InstallOptions{}
}

func TestInstallAddsToUserPath(t *testing.T) {
	// cmd reads these characters as syntax unless they are quoted.
	root := filepath.Join(newRoot(t), "a&b (c)", "bits")
	opts := isolatedOptions(t)
	for _, v := range []string{"v1.2.2", "v1.2.3"} {
		opts.Executable = unpack(t, root, v, true)
		var out strings.Builder
		if err := Install(opts, &out); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = key.Close() }()
	path, kind, err := key.GetStringValue("Path")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, currentLink, "bin")
	entries := filepath.SplitList(path)
	if kind != registry.EXPAND_SZ || entries[0] != bin || strings.Count(strings.ToLower(path), strings.ToLower(bin)) != 1 {
		t.Fatalf("user PATH = %q (type %d), want %s first, once, as REG_EXPAND_SZ", path, kind, bin)
	}
}

func TestInstallKeepsPathThatNamesItThroughVariables(t *testing.T) {
	root := newRoot(t)
	opts := isolatedOptions(t)
	t.Setenv("BITS_TEST_ROOT", root)
	const path = `C:\Tools;%BITS_TEST_ROOT%\current\bin\`
	key, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = key.Close() }()
	if err := key.SetExpandStringValue("Path", path); err != nil {
		t.Fatal(err)
	}
	opts.Executable = unpack(t, root, "v1.2.3", true)
	if err := Install(opts, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, _, err := key.GetStringValue("Path"); err != nil || got != path {
		t.Fatalf("user PATH = %q, %v; want it unchanged: %q", got, err, path)
	}
}

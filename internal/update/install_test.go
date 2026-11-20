package update

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// newRoot returns an installation root with symlinks resolved, as Install
// derives it from the executable.
func newRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "bits")
}

// unpack lays out versions/<v> as an install script leaves it and returns
// its executable.
func unpack(t *testing.T, root, v string, complete bool) string {
	t.Helper()
	dir := filepath.Join(root, versionsDir, v)
	executable := filepath.Join(dir, "bin", executableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if complete {
		if err := os.WriteFile(filepath.Join(dir, completeMarker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return executable
}

func install(t *testing.T, opts InstallOptions, executable string) error {
	t.Helper()
	opts.Executable = executable
	return Install(opts, io.Discard)
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range list {
		names = append(names, entry.Name())
	}
	return names
}

func TestInstallSwitchesVersions(t *testing.T) {
	tests := []struct {
		name      string
		installed []string // installed in order before the last one
		install   string
		want      []string
	}{
		{name: "first install", install: "v1.2.3", want: []string{"v1.2.3"}},
		{name: "keeps the version it replaces", installed: []string{"v1.2.2"}, install: "v1.2.3", want: []string{"v1.2.2", "v1.2.3"}},
		{name: "removes older versions", installed: []string{"v1.2.1", "v1.2.2"}, install: "v1.2.3", want: []string{"v1.2.2", "v1.2.3"}},
		{name: "rolls back", installed: []string{"v1.2.2", "v1.2.3"}, install: "v1.2.2", want: []string{"v1.2.2", "v1.2.3"}},
		{name: "keeps newer versions", installed: []string{"v1.2.1", "v1.2.3", "v1.2.4"}, install: "v1.2.2", want: []string{"v1.2.2", "v1.2.3", "v1.2.4"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			opts := isolatedOptions(t)
			for _, v := range append(tc.installed, tc.install) {
				if err := install(t, opts, unpack(t, root, v, true)); err != nil {
					t.Fatalf("install %s: %v", v, err)
				}
			}
			if got, err := activeVersion(root); err != nil || got != tc.install {
				t.Fatalf("current = %q, %v; want %q", got, err, tc.install)
			}
			if got := entries(t, filepath.Join(root, versionsDir)); !slices.Equal(got, tc.want) {
				t.Fatalf("versions = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstallCleansUpOnlyItsOwnEntries(t *testing.T) {
	root := newRoot(t)
	opts := isolatedOptions(t)
	unpack(t, root, "v1.2.1", false) // a removal that stopped halfway
	unpack(t, root, "v1.2.4", true)  // an upgrade running concurrently
	for _, name := range []string{"notes", "v1.3.0-rc.1"} {
		if err := os.MkdirAll(filepath.Join(root, versionsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, age := range map[string]time.Duration{".staging-old": 2 * stagingExpiry, ".staging-live": 0} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		modified := time.Now().Add(-age)
		if err := os.Chtimes(dir, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	if err := install(t, opts, unpack(t, root, "v1.2.3", true)); err != nil {
		t.Fatal(err)
	}
	if got, want := entries(t, filepath.Join(root, versionsDir)), []string{"notes", "v1.2.3", "v1.2.4", "v1.3.0-rc.1"}; !slices.Equal(got, want) {
		t.Errorf("versions = %q, want %q", got, want)
	}
	if got, want := entries(t, root), []string{".staging-live", currentLink, versionsDir}; !slices.Equal(got, want) {
		t.Errorf("root = %q, want %q", got, want)
	}
}

func TestInstallRefuses(t *testing.T) {
	tests := []struct {
		name       string
		executable func(t *testing.T, root string) string
		want       string
	}{
		{
			name:       "an incomplete version",
			executable: func(t *testing.T, root string) string { return unpack(t, root, "v1.2.4", false) },
			want:       "not a complete installation",
		},
		{
			name:       "a development build",
			executable: func(t *testing.T, root string) string { return unpack(t, root, "dev", true) },
			want:       errNotStandalone.Error(),
		},
		{
			name: "an executable outside an installation",
			executable: func(t *testing.T, root string) string {
				path := filepath.Join(root, executableName)
				if err := os.WriteFile(path, nil, 0o755); err != nil {
					t.Fatal(err)
				}
				return path
			},
			want: errNotStandalone.Error(),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			opts := isolatedOptions(t)
			if err := install(t, opts, unpack(t, root, "v1.2.3", true)); err != nil {
				t.Fatal(err)
			}
			err := install(t, opts, tc.executable(t, root))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Install = %v, want an error containing %q", err, tc.want)
			}
			if got, err := activeVersion(root); err != nil || got != "v1.2.3" {
				t.Fatalf("current = %q, %v; want it left at v1.2.3", got, err)
			}
		})
	}
}

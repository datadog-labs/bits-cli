package update

import (
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// isolatedOptions points Install at a scratch environment key of its own, so
// tests never change the real user PATH, even when they run concurrently.
func isolatedOptions(t *testing.T) InstallOptions {
	t.Helper()
	path := `Software\bits-cli-test-` + rand.Text()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.DeleteKey(registry.CURRENT_USER, path); err != nil {
			t.Errorf("delete %s: %v", path, err)
		}
	})
	return InstallOptions{UserEnvironment: path}
}

func TestInstallAddsToUserPath(t *testing.T) {
	// Characters cmd and PowerShell would read as syntax: the junction and
	// PATH must take the directory name as is.
	root := filepath.Join(newRoot(t), "a&b (c) %PATH% [d]", "bits")
	opts := isolatedOptions(t)
	for _, v := range []string{"v1.2.2", "v1.2.3"} {
		opts.Executable = unpack(t, root, v, true)
		var out strings.Builder
		if err := Install(opts, &out); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, opts.UserEnvironment, registry.QUERY_VALUE)
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
	key, err := registry.OpenKey(registry.CURRENT_USER, opts.UserEnvironment, registry.QUERY_VALUE|registry.SET_VALUE)
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

func TestInstallCompletesAFirstActivation(t *testing.T) {
	tests := []struct {
		name    string
		content bool // the plain current directory holds a file
		want    string
	}{
		{name: "an empty current directory", want: "v1.2.3"},
		{name: "a current directory with files", content: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			current := filepath.Join(root, currentLink)
			if err := os.MkdirAll(current, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.content {
				if err := os.WriteFile(filepath.Join(current, "notes"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := install(t, isolatedOptions(t), unpack(t, root, "v1.2.3", true))
			if (err == nil) != (tc.want != "") {
				t.Fatalf("Install = %v, want success %v", err, tc.want != "")
			}
			if got, err := activeVersion(root); err != nil || got != tc.want {
				t.Fatalf("current = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

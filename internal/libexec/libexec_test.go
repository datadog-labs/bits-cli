package libexec

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirFromEnvironment(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "tool")
	if err := os.Mkdir(want, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(DirEnv, root)
	if got, err := Dir("tool"); err != nil || got != want {
		t.Errorf("Dir(tool) = %q, %v; want %q", got, err, want)
	}
	if _, err := Dir("missing"); err == nil {
		t.Error("Dir(missing) succeeded")
	}
}

func TestDirInExecutablePrefix(t *testing.T) {
	t.Setenv(DirEnv, "")
	// Two releases, as an update leaves them, and a link to the running one.
	var prefixes []string
	for range 2 {
		prefix, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "libexec", "bits", "tool")} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(prefix, "bin", "bits"), nil, 0o755); err != nil {
			t.Fatal(err)
		}
		prefixes = append(prefixes, prefix)
	}
	link := filepath.Join(t.TempDir(), "bits")
	if err := os.Symlink(filepath.Join(prefixes[0], "bin", "bits"), link); err != nil {
		t.Skip(err)
	}
	saved, savedErr := executable, executableErr
	t.Cleanup(func() { executable, executableErr = saved, savedErr })
	executable, executableErr = resolve(link, nil)

	// The update switches the link; this process keeps its own helpers.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(prefixes[1], "bin", "bits"), link); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(prefixes[0], "libexec", "bits", "tool")
	if got, err := Dir("tool"); err != nil || got != want {
		t.Errorf("Dir(tool) = %q, %v; want %q", got, err, want)
	}
}

func TestBinaryResolvesWithoutPreflight(t *testing.T) {
	root := t.TempDir()
	t.Setenv(DirEnv, root)
	name := "missing"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := filepath.Join(root, "missing", name)
	got, err := Binary("missing")
	if err != nil || got != want {
		t.Fatalf("Binary = %q, %v; want %q", got, err, want)
	}
	if err := exec.Command(got).Run(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exec missing helper: %v", err)
	}
}

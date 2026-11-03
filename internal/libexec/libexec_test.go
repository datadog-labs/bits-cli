package libexec

import (
	"os"
	"path/filepath"
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
	prefix := t.TempDir()
	want := filepath.Join(prefix, "libexec", "bits", "tool")
	for _, dir := range []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "link"), want} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bits := filepath.Join(prefix, "bin", "bits")
	if err := os.WriteFile(bits, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(prefix, "link", "bits")
	if err := os.Symlink(bits, link); err != nil {
		t.Skip(err)
	}
	executable = func() (string, error) { return link, nil }
	t.Cleanup(func() { executable = os.Executable })

	got, err := Dir("tool")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ = filepath.EvalSymlinks(want); got != want {
		t.Errorf("Dir(tool) = %q, want %q", got, want)
	}
}

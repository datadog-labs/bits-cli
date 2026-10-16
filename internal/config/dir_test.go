package config

import "testing"

func TestDirIgnoresProcessHomeEnvironment(t *testing.T) {
	before, err := loadDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	after, err := loadDir()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("config directory changed with process environment: %q != %q", before, after)
	}
}

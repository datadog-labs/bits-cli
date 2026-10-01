//go:build linux || darwin

package workspace

import (
	"errors"
	"testing"
)

func TestResolveShell(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured string
		available  map[string]string
		want       string
	}{
		{"configured shell", "/opt/custom/sh", map[string]string{"/opt/custom/sh": "/opt/custom/sh"}, "/opt/custom/sh"},
		{"shell on path", "zsh", map[string]string{"zsh": "/usr/bin/zsh"}, "/usr/bin/zsh"},
		{"missing shell", "/missing/shell", map[string]string{"/bin/sh": "/bin/sh"}, "/bin/sh"},
		{"unsupported shell", "/opt/custom/fish", map[string]string{"/opt/custom/fish": "/opt/custom/fish", "/bin/sh": "/bin/sh"}, "/bin/sh"},
		{"nothing available", "", nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookPath := func(file string) (string, error) {
				if resolved, ok := test.available[file]; ok {
					return resolved, nil
				}
				return "", errors.New("not found")
			}
			if got := resolveShell(test.configured, lookPath); got != test.want {
				t.Fatalf("resolveShell(%q) = %q, want %q", test.configured, got, test.want)
			}
		})
	}
}

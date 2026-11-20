//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolatedOptions gives Install a home of its own and an empty PATH.
func isolatedOptions(t *testing.T) InstallOptions {
	return InstallOptions{Home: t.TempDir()}
}

func TestInstallSetsUpCommand(t *testing.T) {
	bashRC := "~/.bashrc"
	if runtime.GOOS == "darwin" {
		bashRC = "~/.bash_profile"
	}
	tests := []struct {
		name string
		// setup prepares the home and returns the PATH the install sees.
		setup   func(t *testing.T, home string) string
		shell   string
		root    string // base name of the root, after symlinks; default bits
		want    string // advice the first install prints, "" for none
		rerun   string // advice a second install prints
		failing bool
	}{
		{
			name:  "zsh without ~/.local/bin on PATH",
			setup: func(t *testing.T, _ string) string { return t.TempDir() },
			shell: "/bin/zsh",
			want:  `>> "${ZDOTDIR:-$HOME}/.zshrc"`,
			rerun: `>> "${ZDOTDIR:-$HOME}/.zshrc"`,
		},
		{
			name:  "bash without ~/.local/bin on PATH",
			setup: func(*testing.T, string) string { return "" },
			shell: "/usr/local/bin/bash",
			want:  ">> " + bashRC,
			rerun: ">> " + bashRC,
		},
		{
			name:  "fish without ~/.local/bin on PATH",
			setup: func(*testing.T, string) string { return "" },
			shell: "/opt/homebrew/bin/fish",
			want:  "fish_add_path ~/.local/bin",
			rerun: "fish_add_path ~/.local/bin",
		},
		{
			name:  "another shell",
			setup: func(*testing.T, string) string { return "" },
			shell: "/bin/tcsh",
			want:  "to your PATH, then open a new terminal",
			rerun: "to your PATH, then open a new terminal",
		},
		{
			name: "~/.local/bin on PATH",
			setup: func(t *testing.T, home string) string {
				return t.TempDir() + ":" + filepath.Join(home, ".local", "bin") + "/"
			},
			want: "Run bits to get started.",
		},
		{
			name: "another bits first on PATH",
			setup: func(t *testing.T, home string) string {
				other := t.TempDir()
				if err := os.WriteFile(filepath.Join(other, "bits"), nil, 0o755); err != nil {
					t.Fatal(err)
				}
				return other + ":" + filepath.Join(home, ".local", "bin")
			},
			want:  "comes first on your PATH",
			rerun: "comes first on your PATH",
		},
		{
			name: "a link left by a moved installation",
			setup: func(t *testing.T, home string) string {
				link := filepath.Join(home, ".local", "bin", "bits")
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/old/share/bits/current/bin/bits", link); err != nil {
					t.Fatal(err)
				}
				return filepath.Dir(link)
			},
			want: "Run bits to get started.",
		},
		{
			name:  "a data root linked to another name",
			setup: func(t *testing.T, home string) string { return filepath.Join(home, ".local", "bin") },
			root:  "bits-data",
			want:  "Run bits to get started.",
		},
		{
			name: "a bits the installer did not create",
			setup: func(t *testing.T, home string) string {
				link := filepath.Join(home, ".local", "bin", "bits")
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/usr/local/bin/bits", link); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			failing: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			if tc.root != "" {
				root = filepath.Join(filepath.Dir(root), tc.root)
			}
			opts := isolatedOptions(t)
			opts.Path = tc.setup(t, opts.Home)
			opts.Shell = tc.shell
			for run, want := range []string{tc.want, tc.rerun} {
				var out strings.Builder
				opts.Executable = unpack(t, root, "v1.2.3", true)
				err := Install(opts, &out)
				if tc.failing {
					if err == nil || !strings.Contains(err.Error(), "the installer did not create it") {
						t.Fatalf("Install = %v, want a refusal", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("install %d: %v", run+1, err)
				}
				advice := strings.TrimPrefix(out.String(), "Installed Bits v1.2.3 in "+root+"\n")
				if want == "" && advice != "" || !strings.Contains(advice, want) {
					t.Errorf("install %d printed %q, want %q", run+1, advice, want)
				}
			}
			link := filepath.Join(opts.Home, ".local", "bin", "bits")
			if target, err := os.Readlink(link); err != nil || target != filepath.Join(root, currentLink, "bin", "bits") {
				t.Errorf("%s -> %q, %v; want the current bits", link, target, err)
			}
			if _, err := os.Stat(link); err != nil {
				t.Errorf("%s does not resolve: %v", link, err)
			}
		})
	}
}

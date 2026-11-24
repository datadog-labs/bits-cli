//go:build !windows

package scripts

import (
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// isolateUserPath has nothing to do: install.sh only prints PATH advice.
func isolateUserPath(*testing.T) {}

func TestInstallScriptPath(t *testing.T) {
	url := installerServer(t)
	bashRC := "~/.bashrc"
	if runtime.GOOS == "darwin" {
		bashRC = "~/.bash_profile"
	}
	for _, tc := range []struct {
		name     string
		shell    string
		onPath   bool
		other    bool
		existing string
		want     string
		failure  bool
	}{
		{name: "zsh", shell: "/bin/zsh", want: `>> "${ZDOTDIR:-$HOME}/.zshrc"`},
		{name: "bash", shell: "/usr/local/bin/bash", want: ">> " + bashRC},
		{name: "fish", shell: "/opt/homebrew/bin/fish", want: "fish_add_path ~/.local/bin"},
		{name: "unknown shell", shell: "/bin/tcsh", want: "to your PATH, then open a new terminal"},
		{name: "already on PATH", onPath: true, want: "Run bits to get started."},
		{name: "shadowed", onPath: true, other: true, want: "comes first on your PATH"},
		{name: "moved root", onPath: true, existing: "/old/share/bits/current/bin/bits", want: "Run bits to get started."},
		{name: "unrelated link", existing: "/usr/local/bin/bits", want: "the installer did not create it", failure: true},
		{name: "unrelated executable", existing: "file", want: "the installer did not create it", failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			link := filepath.Join(bin, "bits")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.existing == "file" {
				if err := os.WriteFile(link, []byte("keep"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else if tc.existing != "" {
				if err := os.Symlink(tc.existing, link); err != nil {
					t.Fatal(err)
				}
			}
			path := "/usr/bin:/bin"
			if tc.onPath {
				path = bin + "/" + ":" + path
			}
			if tc.other {
				other := t.TempDir()
				if err := os.WriteFile(filepath.Join(other, "bits"), nil, 0o755); err != nil {
					t.Fatal(err)
				}
				path = other + ":" + path
			}
			t.Setenv("PATH", path)
			t.Setenv("SHELL", tc.shell)
			for run := range 2 {
				out, err := runInstaller(t, home, url, "v0.1.0")
				if (err != nil) != tc.failure {
					t.Fatalf("install: %v\n%s", err, out)
				}
				want := tc.want
				if run == 1 && tc.onPath && !tc.other {
					want = ""
				}
				advice := strings.SplitAfter(out, "Installed Bits v0.1.0 in ")[1:]
				if !strings.Contains(out, want) || want == "" && len(advice) > 0 && strings.Count(out, "\n") != 2 {
					t.Fatalf("install %d = %q, want %q", run, out, want)
				}
			}
			if tc.failure {
				return
			}
			resolved, err := filepath.EvalSymlinks(link)
			root, rootErr := filepath.EvalSymlinks(filepath.Join(home, ".local", "share", "bits"))
			if err != nil || rootErr != nil || resolved != filepath.Join(root, "versions", "v0.1.0", "bin", "bits") {
				t.Fatalf("link=%q, %v; root=%q, %v", resolved, err, root, rootErr)
			}
			for _, rc := range []string{".zshrc", ".bashrc", ".bash_profile", ".profile"} {
				if _, err := os.Stat(filepath.Join(home, rc)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("installer edited %s: %v", rc, err)
				}
			}
		})
	}
}

func TestInstallScriptRefusesHTTPSDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "untrusted checksums\n") }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, plain.URL, http.StatusFound) }))
	defer secure.Close()
	cert := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, client := range []string{"curl", "wget"} {
		t.Run(client, func(t *testing.T) {
			tools := t.TempDir()
			for _, name := range []string{client, "sh", "uname", "grep", "awk", "mkdir", "mktemp", "rm", "sed", "tr", "tail", "sysctl"} {
				path, err := exec.LookPath(name)
				if err != nil {
					if name == client {
						t.Skipf("%s is unavailable: %v", client, err)
					}
					if name == "sysctl" {
						continue
					}
					t.Fatal(err)
				}
				if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", tools)
			t.Setenv("CURL_CA_BUNDLE", cert)
			config := filepath.Join(t.TempDir(), "wgetrc")
			if err := os.WriteFile(config, []byte("ca_certificate = "+cert+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WGETRC", config)
			home := t.TempDir()
			// Use an absolute shell because PATH deliberately contains only one client.
			cmd := installerCommand(home, secure.URL, "v0.1.0")
			cmd.Path = "/bin/sh"
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(strings.ToLower(string(out)), "redirect") {
				t.Fatalf("insecure redirect accepted or wrong failure: %v\n%s", err, out)
			}
		})
	}
}

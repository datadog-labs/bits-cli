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
	"sync/atomic"
	"testing"
)

// isolateUserPath has nothing to do: install.sh only prints PATH advice.
func isolateUserPath(*testing.T) {}

func installerArch(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" {
		// The sysctl is absent on Intel Macs.
		out, err := exec.Command("sysctl", "-n", "sysctl.proc_translated").Output()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return "arm64"
		}
	}
	return runtime.GOARCH
}

func resetInstallCommand(t *testing.T, home string) string {
	t.Helper()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.Remove(filepath.Join(bin, "bits")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return "Run bits to get started."
}

func TestInstallScriptPath(t *testing.T) {
	url := installerServer(t)
	tools := t.TempDir()
	// CI images can ship an unrelated bits in /usr/bin.
	names := []string{"sh", "curl", "uname", "grep", "awk", "mkdir", "mktemp", "rm", "tar", "gzip", "mv", "ln", "readlink"}
	if runtime.GOOS == "darwin" {
		names = append(names, "sysctl", "shasum")
	} else {
		names = append(names, "flock", "sha256sum")
	}
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
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
		{name: "bin directory is a file", existing: "parent file", want: "mkdir", failure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".local", "bin")
			link := filepath.Join(bin, "bits")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.existing == "parent file" {
				if err := os.Remove(bin); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bin, []byte("keep"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if tc.existing == "file" {
				if err := os.WriteFile(link, []byte("keep"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else if tc.existing != "" {
				if err := os.Symlink(tc.existing, link); err != nil {
					t.Fatal(err)
				}
			}
			path := tools
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
				if run == 1 && tc.onPath && !tc.other {
					if strings.Contains(out, tc.want) {
						t.Fatalf("reinstall repeated command setup advice: %s", out)
					}
				} else if !strings.Contains(out, tc.want) {
					t.Fatalf("install %d = %q, want %q", run, out, tc.want)
				}
			}
			if tc.failure {
				current := filepath.Join(home, ".local", "share", "bits", "current")
				if _, err := os.Lstat(current); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("conflicting install changed current: %v", err)
				}
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

func TestInstallScriptUpdatePreservesCommand(t *testing.T) {
	url := installerServer(t)
	dir := t.TempDir()
	home, oldRoot, selectedRoot := filepath.Join(dir, "home"), filepath.Join(dir, "old", "bits"), filepath.Join(dir, "selected", "bits")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := runInstaller(t, home, url, "v0.1.0", "--root", selectedRoot); err != nil {
		t.Fatalf("install selected root: %v\n%s", err, out)
	}
	if err := os.MkdirAll(oldRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("versions/v0.0.15", filepath.Join(oldRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if out, err := runInstaller(t, home, url, "v0.1.0", "--root", oldRoot, "--update-from", "v0.0.15"); err != nil {
		t.Fatalf("update previous root: %v\n%s", err, out)
	}
	if got, err := os.Readlink(filepath.Join(oldRoot, "current")); err != nil || got != "versions/v0.1.0" {
		t.Fatalf("previous root was not updated: current = %q, %v", got, err)
	}
	physicalRoot, err := filepath.EvalSymlinks(selectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(physicalRoot, "current", "bin", "bits")
	if got, err := os.Readlink(filepath.Join(home, ".local", "bin", "bits")); err != nil || got != want {
		t.Fatalf("command link = %q, %v, want %q", got, err, want)
	}
	// A conflict blocks an explicit install before it changes the active
	// release. Updating through the old root's full path can still succeed.
	command := filepath.Join(home, ".local", "bin", "bits")
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(command, []byte("unrelated"), 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(oldRoot, "current")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("versions/v0.0.15", current); err != nil {
		t.Fatal(err)
	}
	if out, err := runInstaller(t, home, url, "v0.1.0", "--root", oldRoot); err == nil || !strings.Contains(out, "the installer did not create it") {
		t.Fatalf("manual install should reject command conflict: %v\n%s", err, out)
	}
	if got, err := os.Readlink(current); err != nil || got != "versions/v0.0.15" {
		t.Fatalf("manual install changed current to %q: %v", got, err)
	}
	if out, err := runInstaller(t, home, url, "v0.1.0", "--root", oldRoot, "--update-from", "v0.0.15"); err != nil {
		t.Fatalf("update with unrelated command: %v\n%s", err, out)
	}
	if got, err := os.Readlink(current); err != nil || got != "versions/v0.1.0" {
		t.Fatalf("update did not activate release: current = %q, %v", got, err)
	}
	if got, err := os.ReadFile(command); err != nil || string(got) != "unrelated" {
		t.Fatalf("update changed unrelated command: %q, %v", got, err)
	}
}

func TestInstallScriptRefusesHTTPSDowngrade(t *testing.T) {
	var plainRequests, secureRequests atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainRequests.Add(1)
		_, _ = fmt.Fprint(w, "untrusted checksums\n")
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secureRequests.Add(1)
		// HTTP permits omitting the space after the field name.
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}()
		if _, err := fmt.Fprintf(buf, "HTTP/1.1 302 Found\r\nlocation:%s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", plain.URL); err != nil {
			t.Error(err)
			return
		}
		if err := buf.Flush(); err != nil {
			t.Error(err)
		}
	}))
	defer secure.Close()
	cert := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CURL_CA_BUNDLE", cert)
	for _, version := range []string{"", "v0.1.0"} {
		plainBefore, secureBefore := plainRequests.Load(), secureRequests.Load()
		out, err := runInstaller(t, t.TempDir(), secure.URL, version)
		if err == nil || secureRequests.Load() == secureBefore {
			t.Fatalf("installer did not refuse the downgrade after reaching HTTPS: %v\n%s", err, out)
		}
		if plainRequests.Load() != plainBefore {
			t.Fatal("curl followed the downgrade to HTTP")
		}
	}
}

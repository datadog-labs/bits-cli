package scripts

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// releaseArchive builds bits from this checkout as release v and packs it
// as the release archive for this platform.
func releaseArchive(t *testing.T, v string, flags ...string) (name string, content []byte) {
	t.Helper()
	arch := installerArch(t)
	executable := "bits"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	built := filepath.Join(t.TempDir(), executable)
	build := exec.Command("go", "build", "-o", built,
		"-ldflags", strings.Join(append(flags, "-X github.com/datadog-labs/bits-cli/internal/version.releaseVersion="+v), " "), ".")
	build.Env = append(os.Environ(), "GOARCH="+arch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	bits, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	name = fmt.Sprintf("bits_%s_%s_%s", v, runtime.GOOS, arch)
	if runtime.GOOS == "windows" {
		w := zip.NewWriter(&buf)
		f, err := w.Create("bin/" + executable)
		if err == nil {
			_, err = f.Write(bits)
		}
		if err == nil {
			err = w.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		return name + ".zip", buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	err = w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "bin/", Mode: 0o755})
	if err == nil {
		err = w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "bin/bits", Mode: 0o755, Size: int64(len(bits))})
	}
	if err == nil {
		_, err = w.Write(bits)
	}
	if err == nil {
		err = w.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return name + ".tar.gz", buf.Bytes()
}

// runInstaller runs this platform's install script as a user would, with
// its home and data in dir, against the releases at url.
func runInstaller(t *testing.T, dir, url, v string, args ...string) (string, error) {
	t.Helper()
	out, err := installerCommand(dir, url, v, args...).CombinedOutput()
	return string(out), err
}

func installerCommand(dir, url, v string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// The way the README runs it, which is where exit and encoding bugs show.
		if len(args) == 0 {
			cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "irm -MaximumRedirection 0 "+url+"/install.ps1 | iex")
		} else {
			cmd = exec.Command("powershell.exe", append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", "scripts/install.ps1"}, args...)...)
		}
	} else {
		cmd = exec.Command("sh", append([]string{"scripts/install.sh"}, args...)...)
	}
	cmd.Env = append(os.Environ(),
		"HOME="+dir, "XDG_DATA_HOME=", "LOCALAPPDATA="+dir,
		"BITS_VERSION="+v, "BITS_RELEASES_URL="+url+"/releases", "BITS_LIBEXEC_DIR=")
	return cmd
}

func TestInstallScript(t *testing.T) {
	archive, content := releaseArchive(t, "0.1.0")
	sum := fmt.Sprintf("%x", sha256.Sum256(content))
	tests := []struct {
		name          string
		version       string // BITS_VERSION, "" for the latest release
		latest        string // the release releases/latest redirects to
		checksum      string
		failure       string // "" for a successful install
		entry         string
		symlink       bool
		binaryVersion string
	}{
		{name: "the latest release", latest: "v0.1.0", checksum: sum},
		{name: "a latest release older than the installer", latest: "v0.0.14", checksum: sum, failure: "predates this installer"},
		{name: "installs", version: "v0.1.0", checksum: sum},
		{name: "a version without v", version: "0.1.0", checksum: sum},
		{name: "wrong binary version", version: "v0.1.0", binaryVersion: "0.1.1", failure: "reported"},
		{name: "unsafe archive", version: "v0.1.0", entry: "../../escaped", failure: "archive path"},
		{name: "symlink archive", version: "v0.1.0", entry: "bin/bits", symlink: true, failure: "archive entry"},
		{name: "missing executable", version: "v0.1.0", entry: "share/README.md", failure: "no executable"},
		{name: "corrupt download", version: "v0.1.0", checksum: strings.Repeat("0", 64), failure: "does not match its checksum"},
		{name: "unpublished version", version: "v0.2.0", checksum: sum, failure: "404"},
		{name: "a release before the supported archives", version: "v0.0.14", checksum: sum, failure: "predates this installer"},
		{name: "prerelease", version: "v0.1.0-rc.1", failure: "not a release version"},
		{name: "path traversal version", version: "../../other", failure: "not a release version"},
		{name: "noncanonical version", version: "v01.0.0", failure: "not a release version"},
		{name: "latest prerelease", latest: "v0.1.0-rc.1", failure: "not a release version"},
		{name: "latest is not a tag", latest: "", failure: "latest release"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserPath(t)
			content := content
			if tc.binaryVersion != "" {
				_, content = releaseArchive(t, tc.binaryVersion)
				tc.checksum = fmt.Sprintf("%x", sha256.Sum256(content))
			}

			if tc.entry != "" {
				var buf bytes.Buffer
				if runtime.GOOS == "windows" {
					w := zip.NewWriter(&buf)
					header := &zip.FileHeader{Name: tc.entry, Method: zip.Deflate}
					if tc.symlink {
						header.SetMode(os.ModeSymlink | 0o777)
					}
					entry, err := w.CreateHeader(header)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := entry.Write([]byte("outside")); err != nil {
						t.Fatal(err)
					}
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					gz := gzip.NewWriter(&buf)
					w := tar.NewWriter(gz)
					kind := byte(tar.TypeReg)
					if tc.symlink {
						kind = tar.TypeSymlink
					}
					if err := w.WriteHeader(&tar.Header{Name: tc.entry, Typeflag: kind, Mode: 0o755, Linkname: "outside"}); err != nil {
						t.Fatal(err)
					}
					if err := w.Close(); err != nil {
						t.Fatal(err)
					}
					if err := gz.Close(); err != nil {
						t.Fatal(err)
					}
				}
				content = buf.Bytes()
				tc.checksum = fmt.Sprintf("%x", sha256.Sum256(content))
			}

			mux := http.NewServeMux()
			mux.HandleFunc("GET /install.ps1", func(w http.ResponseWriter, r *http.Request) {
				http.ServeFile(w, r, "scripts/install.ps1")
			})
			mux.HandleFunc("GET /releases/latest", func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/releases/tag/"+tc.latest, http.StatusFound)
			})
			mux.HandleFunc("GET /releases/tag/", func(http.ResponseWriter, *http.Request) {})
			mux.HandleFunc("GET /releases/download/v0.1.0/bits_0.1.0_checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, "%s  %s.sbom.json\n%s  %s\n", strings.Repeat("0", 64), archive, tc.checksum, archive)
			})
			mux.HandleFunc("GET /releases/download/v0.1.0/"+archive, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(content)
			})
			server := httptest.NewServer(mux)
			t.Cleanup(server.Close)
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			// Characters shells, cmd, and PowerShell would read as syntax.
			dir = filepath.Join(dir, "a&b (c) %PATH% [d $e")
			if runtime.GOOS != "windows" {
				dir += `\backslash`
			}
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(dir, ".local", "share", "bits")
			command := filepath.Join(dir, ".local", "bin", "bits")
			if runtime.GOOS == "windows" {
				root = filepath.Join(dir, "bits")
				command = filepath.Join(root, "current", "bin", "bits.exe")
			}

			// Installing again finds the version in place.
			for range 2 {
				out, err := runInstaller(t, dir, server.URL, tc.version)
				if tc.failure != "" {
					if err == nil || !strings.Contains(out, tc.failure) {
						t.Fatalf("install = %v, want a failure with %q:\n%s", err, tc.failure, out)
					}
					if entries, err := os.ReadDir(root); err == nil {
						for _, entry := range entries {
							if entry.Name() != "versions" && entry.Name() != "install.lock" {
								t.Fatalf("failed install left %s", entry.Name())
							}
						}
					}
					if _, err := os.Stat(filepath.Join(root, "escaped")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("archive escaped staging: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("install: %v\n%s", err, out)
				}
			}
			out, err := exec.Command(command, "--version").CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "bits version v0.1.0" {
				t.Fatalf("%s --version = %q, %v", command, out, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 3 {
				t.Fatalf("%s holds %v, %v; want current and versions only", root, entries, err)
			}
		})
	}
}

// PowerShell 5.1 decodes a downloaded script with the system code page.
func TestInstallScriptsAreASCII(t *testing.T) {
	for _, name := range []string{"scripts/install.sh", "scripts/install.ps1"} {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, b := range content {
			if b >= 0x80 {
				t.Fatalf("%s has a non-ASCII byte at offset %d", name, i)
			}
		}
	}
}

func TestInstallScriptLifecycle(t *testing.T) {
	var latest atomic.Value
	var downloadsBlocked atomic.Bool
	latest.Store("v0.1.2")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /scripts/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "scripts/"+strings.TrimPrefix(r.URL.Path, "/scripts/"))
	})
	mux.HandleFunc("GET /install.ps1", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "scripts/install.ps1")
	})
	mux.HandleFunc("GET /releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+latest.Load().(string), http.StatusFound)
	})
	mux.HandleFunc("GET /releases/tag/", func(http.ResponseWriter, *http.Request) {})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if downloadsBlocked.Load() && strings.HasPrefix(r.URL.Path, "/releases/download/") {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	for _, v := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		archive, data := releaseArchive(t, v,
			"-X github.com/datadog-labs/bits-cli/internal/update.releasesURL="+server.URL+"/releases",
			"-X github.com/datadog-labs/bits-cli/internal/update.installerBaseURL="+server.URL+"/scripts")
		mux.HandleFunc("GET /releases/download/v"+v+"/"+archive, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) })
		mux.HandleFunc("GET /releases/download/v"+v+"/bits_"+v+"_checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(data), archive)
		})
	}
	tests := []struct {
		name      string
		installed []string
		version   string
		want      []string
		update    bool
		stale     bool
		staging   bool
		latest    string
	}{
		{name: "first install", version: "v0.1.0", want: []string{"v0.1.0"}},
		{name: "keeps previous", installed: []string{"v0.1.0"}, version: "v0.1.1", want: []string{"v0.1.0", "v0.1.1"}},
		{name: "leaves older releases and other staging alone", staging: true, installed: []string{"v0.1.0", "v0.1.1"}, version: "v0.1.2", want: []string{"v0.1.0", "v0.1.1", "v0.1.2"}},
		{name: "unpinned install preserves newer active release", installed: []string{"v0.1.2"}, latest: "v0.1.1", version: "v0.1.2", want: []string{"v0.1.2"}},
		{name: "rollback", installed: []string{"v0.1.0", "v0.1.1"}, version: "v0.1.0", want: []string{"v0.1.0", "v0.1.1"}},
		{name: "reinstall keeps all versions", installed: []string{"v0.1.0", "v0.1.1", "v0.1.2"}, version: "v0.1.2", want: []string{"v0.1.0", "v0.1.1", "v0.1.2"}},
		{name: "pinned install between two existing releases", installed: []string{"v0.1.0", "v0.1.2"}, version: "v0.1.1", want: []string{"v0.1.0", "v0.1.1", "v0.1.2"}},
		{name: "CLI update uses the installed root and explicit version", installed: []string{"v0.1.0"}, version: "v0.1.2", want: []string{"v0.1.0", "v0.1.2"}, update: true},
		{name: "old session cannot undo a manual rollback", installed: []string{"v0.1.0", "v0.1.1", "v0.1.0"}, version: "v0.1.0", want: []string{"v0.1.0", "v0.1.1"}, update: true, stale: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserPath(t)
			downloadsBlocked.Store(false)
			latest.Store("v0.1.2")
			requested := tc.version
			if tc.latest != "" {
				latest.Store(tc.latest)
				requested = ""
			}
			home := t.TempDir()
			root := filepath.Join(home, ".local", "share", "bits")
			executable := "bits"
			if runtime.GOOS == "windows" {
				root = filepath.Join(home, "bits")
				executable += ".exe"
			}
			for _, v := range tc.installed {
				if out, err := runInstaller(t, home, server.URL, v); err != nil {
					t.Fatalf("install %s: %v\n%s", v, err, out)
				}
			}
			var wantCommandSetup string
			if tc.latest != "" {
				wantCommandSetup = resetInstallCommand(t, home)
			}
			downloadsBlocked.Store(tc.latest != "" || tc.stale)
			var wantStaging []string
			if tc.staging {
				dir := filepath.Join(root, ".staging-old")
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				expired := time.Now().Add(-48 * time.Hour)
				if err := os.Chtimes(dir, expired, expired); err != nil {
					t.Fatal(err)
				}
				wantStaging = []string{dir}
			}
			if tc.update {
				from := "v0.1.0"
				if tc.stale {
					from = "v0.1.1"
				}
				invoked := filepath.Join(root, "current", "bin", executable)
				if tc.stale {
					invoked = filepath.Join(root, "versions", from, "bin", executable)
				}
				cmd := exec.Command(invoked, "update")
				cmd.Env = append(os.Environ(), "HOME="+home, "XDG_DATA_HOME="+filepath.Join(home, "wrong"), "LOCALAPPDATA="+filepath.Join(home, "wrong"), "BITS_VERSION=v0.0.1", "BITS_RELEASES_URL=https://wrong.invalid", "BITS_CONFIG_DIR="+filepath.Join(home, "config"))
				if runtime.GOOS == "windows" {
					cmd.Env = append(cmd.Env, "BITS_TEST_USER_ENVIRONMENT="+os.Getenv("BITS_TEST_USER_ENVIRONMENT")+"\\nonexistent")
				}
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("update: %v\n%s", err, out)
				}
			} else {
				out, err := runInstaller(t, home, server.URL, requested)
				if err != nil || !strings.Contains(out, wantCommandSetup) {
					t.Fatalf("install: %v, expected output to contain %q\n%s", err, wantCommandSetup, out)
				}
			}
			out, err := exec.Command(filepath.Join(root, "current", "bin", executable), "--version").CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "bits version "+tc.version {
				t.Fatalf("current: %q, %v", out, err)
			}
			staging, err := filepath.Glob(filepath.Join(root, ".staging-*"))
			if err != nil || !slices.Equal(staging, wantStaging) {
				t.Fatalf("staging = %v, want %v: %v", staging, wantStaging, err)
			}
			var versions []string
			entries, err := os.ReadDir(filepath.Join(root, "versions"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "v0.1.") {
					versions = append(versions, entry.Name())
				}
			}
			if !slices.Equal(versions, tc.want) {
				t.Fatalf("versions = %v, want %v", versions, tc.want)
			}
		})
	}
	t.Run("concurrent same-version installs", func(t *testing.T) {
		downloadsBlocked.Store(false)
		isolateUserPath(t)
		home := t.TempDir()
		var commands []*exec.Cmd
		var outputs []*bytes.Buffer
		for range 4 {
			cmd := installerCommand(home, server.URL, "v0.1.2")
			out := new(bytes.Buffer)
			cmd.Stdout = out
			cmd.Stderr = out
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			commands = append(commands, cmd)
			outputs = append(outputs, out)
		}
		for i, cmd := range commands {
			if err := cmd.Wait(); err != nil {
				t.Errorf("install %d: %v\n%s", i, err, outputs[i])
			}
		}
	})
}

func TestInstallScriptTruncated(t *testing.T) {
	name, call := "install.sh", "main"
	if runtime.GOOS == "windows" {
		name, call = "install.ps1", "Install-Bits"
	}
	source, err := os.ReadFile(filepath.Join("scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	cut := strings.LastIndex(string(source), call) + len(call)
	if cut < len(call) {
		t.Fatal("installer invocation not found")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); http.NotFound(w, r) }))
	defer server.Close()
	home := t.TempDir()
	file := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(file, source[:cut], 0o600); err != nil {
		t.Fatal(err)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file)
	} else {
		cmd = exec.Command("sh", file)
	}
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_DATA_HOME=", "LOCALAPPDATA="+home, "BITS_VERSION=v0.1.0", "BITS_RELEASES_URL="+server.URL)
	out, err := cmd.CombinedOutput()
	if err == nil || requests.Load() != 0 {
		t.Fatalf("truncated installer ran: requests=%d, err=%v\n%s", requests.Load(), err, out)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("truncated installer changed home: %v, %v", entries, err)
	}
}

// installerServer serves a complete release and the real scripts without GitHub.
func installerServer(t *testing.T) string {
	t.Helper()
	archive, content := releaseArchive(t, "0.1.0")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /install.ps1", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, "scripts/install.ps1") })
	mux.HandleFunc("GET /releases/download/v0.1.0/"+archive, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(content) })
	mux.HandleFunc("GET /releases/download/v0.1.0/bits_0.1.0_checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(content), archive)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

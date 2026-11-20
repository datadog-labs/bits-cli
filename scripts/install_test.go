package scripts

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
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

// releaseArchive builds bits from this checkout as release v and packs it
// as the release archive for this platform.
func releaseArchive(t *testing.T, v string) (name string, content []byte) {
	t.Helper()
	executable := "bits"
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	built := filepath.Join(t.TempDir(), executable)
	build := exec.Command("go", "build", "-o", built,
		"-ldflags", "-X github.com/datadog-labs/bits-cli/internal/version.releaseVersion="+v, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	bits, err := os.ReadFile(built)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	name = fmt.Sprintf("bits_%s_%s_%s", v, runtime.GOOS, runtime.GOARCH)
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
func runInstaller(t *testing.T, dir, url, v string) (string, error) {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// The way the README runs it, which is where exit and encoding bugs show.
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "irm "+url+"/install.ps1 | iex")
	} else {
		cmd = exec.Command("sh", "scripts/install.sh")
	}
	cmd.Env = append(os.Environ(),
		"HOME="+dir, "XDG_DATA_HOME=", "LOCALAPPDATA="+dir,
		"BITS_VERSION="+v, "BITS_RELEASES_URL="+url+"/releases", "BITS_LIBEXEC_DIR=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallScript(t *testing.T) {
	archive, content := releaseArchive(t, "0.1.0")
	sum := fmt.Sprintf("%x", sha256.Sum256(content))
	tests := []struct {
		name     string
		version  string
		checksum string
		failure  string // "" for a successful install
	}{
		{name: "installs", version: "v0.1.0", checksum: sum},
		{name: "a version without v", version: "0.1.0", checksum: sum},
		{name: "corrupt download", version: "v0.1.0", checksum: strings.Repeat("0", 64), failure: "does not match its checksum"},
		{name: "unpublished version", version: "v0.2.0", checksum: sum, failure: "404"},
		{name: "a release without bits install", version: "v0.0.14", checksum: sum, failure: "predates this installer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isolateUserPath(t)
			mux := http.NewServeMux()
			mux.HandleFunc("GET /install.ps1", func(w http.ResponseWriter, r *http.Request) {
				http.ServeFile(w, r, "scripts/install.ps1")
			})
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
			dir = filepath.Join(dir, "a&b (c) %PATH% [d] $e")
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
					if entries, err := os.ReadDir(root); err == nil && len(entries) != 1 {
						t.Fatalf("a failed install left %d entries in %s, want only versions", len(entries), root)
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
			if err != nil || len(entries) != 2 {
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

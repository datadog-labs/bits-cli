package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// TestMain lets upgrade tests ship this test binary as the bits of a
// release: run as bits install, it installs itself.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == executableName && len(os.Args) == 2 && os.Args[1] == "install" {
		executable, err := os.Executable()
		if err == nil {
			err = Install(InstallOptions{Executable: executable, Home: os.Getenv("HOME")}, os.Stderr)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// releaseArchive packs this test binary as bin/bits in this platform's
// release archive format.
var releaseArchive = sync.OnceValues(func() ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	bits, err := os.ReadFile(executable)
	if err != nil {
		return nil, err
	}
	files := []struct {
		name string
		mode int64
		body []byte
	}{
		{"bin/" + executableName, 0o755, bits},
		{"share/doc/bits/README.md", 0o644, []byte("# Bits\n")},
	}
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		w := zip.NewWriter(&buf)
		for _, file := range files {
			f, err := w.Create(file.name)
			if err != nil {
				return nil, err
			}
			if _, err := f.Write(file.body); err != nil {
				return nil, err
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	for _, dir := range []string{"bin/", "share/", "share/doc/", "share/doc/bits/"} {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: dir, Mode: 0o755}); err != nil {
			return nil, err
		}
	}
	for _, file := range files {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: file.name, Mode: file.mode, Size: int64(len(file.body))}); err != nil {
			return nil, err
		}
		if _, err := w.Write(file.body); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
})

// fakeRelease serves v as the latest release, with assets for this platform.
type fakeRelease struct {
	latest   string
	archive  []byte
	checksum string // overrides the archive's SHA-256
	missing  bool   // the archive is not uploaded
}

func (f *fakeRelease) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	archive := archiveName(f.latest)
	switch r.URL.Path {
	case "/releases/latest":
		http.Redirect(w, r, "/releases/tag/"+f.latest, http.StatusFound)
	case "/releases/download/" + f.latest + "/" + checksumsName(f.latest):
		sum := f.checksum
		if sum == "" {
			sum = fmt.Sprintf("%x", sha256.Sum256(f.archive))
		}
		_, _ = fmt.Fprintf(w, "%s  %s.sbom.json\n%s  %s\n", strings.Repeat("0", 64), archive, sum, archive)
	case "/releases/download/" + f.latest + "/" + archive:
		if f.missing {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(f.archive)
	default:
		http.NotFound(w, r)
	}
}

func TestStandaloneUpgrade(t *testing.T) {
	archive, err := releaseArchive()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		release fakeRelease
		want    string // the active version afterwards
		output  string
		failure string
		// existing leaves versions/v1.2.4 complete, as a concurrent upgrade
		// does, or incomplete, as a removal that stopped halfway does.
		existing string
	}{
		{name: "upgrades", release: fakeRelease{latest: "v1.2.4"}, want: "v1.2.4", output: "Installed Bits v1.2.4"},
		{name: "up to date", release: fakeRelease{latest: "v1.2.3"}, want: "v1.2.3", output: "Bits v1.2.3 is up to date."},
		{name: "never downgrades", release: fakeRelease{latest: "v1.2.2"}, want: "v1.2.3", output: "Bits v1.2.3 is up to date."},
		{name: "corrupt download", release: fakeRelease{latest: "v1.2.4", checksum: strings.Repeat("ab", 32)}, want: "v1.2.3", failure: "does not match its checksum"},
		{name: "missing archive", release: fakeRelease{latest: "v1.2.4", missing: true}, want: "v1.2.3", failure: "HTTP 404"},
		{name: "keeps a concurrent upgrade", release: fakeRelease{latest: "v1.2.4"}, want: "v1.2.4", output: "Installed Bits v1.2.4", existing: "complete"},
		{name: "replaces a half-removed version", release: fakeRelease{latest: "v1.2.4"}, want: "v1.2.4", output: "Installed Bits v1.2.4", existing: "incomplete"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newRoot(t)
			opts := isolatedOptions(t)
			opts.Home = t.TempDir()
			t.Setenv("HOME", opts.Home)
			if err := install(t, opts, unpack(t, root, "v1.2.3", true)); err != nil {
				t.Fatal(err)
			}
			leftover := filepath.Join(root, versionsDir, "v1.2.4", "leftover")
			if tc.existing != "" {
				shipTestBinary(t, unpack(t, root, "v1.2.4", tc.existing == "complete"))
				if err := os.WriteFile(leftover, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			tc.release.archive = archive
			server := httptest.NewServer(&tc.release)
			t.Cleanup(server.Close)

			var stderr bytes.Buffer
			err := newStandalone(server.URL+"/releases", root, "v1.2.3").Upgrade(nil, io.Discard, &stderr)
			if tc.failure == "" && err != nil || tc.failure != "" && (err == nil || !strings.Contains(err.Error(), tc.failure)) {
				t.Fatalf("Upgrade = %v, want failure %q; stderr:\n%s", err, tc.failure, &stderr)
			}
			if !strings.Contains(stderr.String(), tc.output) {
				t.Errorf("stderr = %q, want %q", &stderr, tc.output)
			}
			if got, err := activeVersion(root); err != nil || got != tc.want {
				t.Errorf("current = %q, %v; want %q", got, err, tc.want)
			}
			wantVersions := []string{"v1.2.3"}
			if tc.want != "v1.2.3" {
				wantVersions = append(wantVersions, tc.want)
			}
			if got := entries(t, filepath.Join(root, versionsDir)); !slices.Equal(got, wantVersions) {
				t.Errorf("versions = %q, want %q", got, wantVersions)
			}
			if got := entries(t, root); !slices.Equal(got, []string{currentLink, versionsDir}) {
				t.Errorf("root = %q, want no staging left", got)
			}
			if _, err := os.Stat(leftover); tc.existing != "" && (err == nil) != (tc.existing == "complete") {
				t.Errorf("%s exists = %v, want it kept only in a complete version", leftover, err == nil)
			}
		})
	}
}

// shipTestBinary copies this test binary to executable, so running it as
// bits install works.
func shipTestBinary(t *testing.T, executable string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bits, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, bits, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneLatestVersion(t *testing.T) {
	tests := []struct {
		name     string
		redirect map[string]string // path to Location; other paths get HTTP 500
		want     string
		invalid  bool
	}{
		{name: "latest release", redirect: map[string]string{"/releases/latest": "/releases/tag/v1.2.4"}, want: "v1.2.4"},
		{
			name: "renamed repository",
			redirect: map[string]string{
				"/releases/latest":         "/renamed/releases/latest",
				"/renamed/releases/latest": "/renamed/releases/tag/v1.2.4",
			},
			want: "v1.2.4",
		},
		{name: "no release", redirect: map[string]string{"/releases/latest": "/releases"}},
		{name: "prerelease", redirect: map[string]string{"/releases/latest": "/releases/tag/v1.3.0-rc.1"}, invalid: true},
		{name: "other host", redirect: map[string]string{"/releases/latest": "https://example.invalid/releases/tag/v1.2.4"}},
		{name: "server error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if location, ok := tc.redirect[r.URL.Path]; ok {
					http.Redirect(w, r, location, http.StatusMovedPermanently)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			got, err := newStandalone(server.URL+"/releases", "", "").LatestVersion(context.Background())
			if got != tc.want || (err == nil) != (tc.want != "") || errors.Is(err, errInvalidVersion) != tc.invalid {
				t.Fatalf("LatestVersion = %q, %v; want %q (invalid version %v)", got, err, tc.want, tc.invalid)
			}
		})
	}
}

func TestStandaloneStaysOnHTTPS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/releases/tag/v1.2.4", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	s := newStandalone(server.URL+"/releases", "", "")
	s.check.Transport = server.Client().Transport
	s.download.Transport = server.Client().Transport
	if _, err := s.LatestVersion(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Errorf("LatestVersion = %v, want a refused redirect", err)
	}
	if _, err := s.get(context.Background(), "v1.2.4", "bits_1.2.4_checksums.txt"); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Errorf("get = %v, want a refused redirect", err)
	}
}

func TestDetectStandalone(t *testing.T) {
	root := newRoot(t)
	opts := isolatedOptions(t)
	if runtime.GOOS != "windows" {
		opts.Home = t.TempDir()
	}
	executable := unpack(t, root, "v1.2.3", true)
	if err := install(t, opts, executable); err != nil {
		t.Fatal(err)
	}
	incomplete := unpack(t, root, "v1.2.4", false)
	for path, want := range map[string]bool{
		executable: true,
		filepath.Join(root, currentLink, "bin", executableName): true,
		incomplete: false,
	} {
		got, err := detectBackend(path)
		if !want {
			if got != nil || !errors.Is(err, ErrUnsupportedInstallation) {
				t.Errorf("detect %s = %v, %v; want unsupported installation", path, got, err)
			}
			continue
		}
		if s, ok := got.(standalone); err != nil || !ok || s.root != root || s.current != "v1.2.3" {
			t.Errorf("detect %s = %#v, %v; want standalone v1.2.3 in %s", path, got, err, root)
		}
	}
}

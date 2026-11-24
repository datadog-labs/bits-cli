package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStandaloneUpgrade(t *testing.T) {
	tests := []struct {
		name        string
		latest      string
		status      int
		interrupted bool
		duringRun   bool
		truncated   bool
		exit        int
		want        string
	}{
		{name: "upgrades", latest: "v1.2.4", status: 200},
		{name: "up to date", latest: "v1.2.3", want: "is up to date"},
		{name: "never downgrades", latest: "v1.2.2", want: "is up to date"},
		{name: "installer missing", latest: "v1.2.4", status: 404, want: "HTTP 404"},
		{name: "interrupted download", latest: "v1.2.4", status: 200, interrupted: true, want: "context canceled"},
		{name: "truncated download", latest: "v1.2.4", status: 200, truncated: true, want: "unexpected EOF"},
		{name: "cancels a running installer", latest: "v1.2.4", status: 200, duringRun: true, want: "context canceled"},
		{name: "installer failure", latest: "v1.2.4", status: 200, exit: 7, want: "exit status 7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "a&b (c) %PATH% [d] $e")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(dir, "invocation")
			t.Setenv("BITS_TEST_CAPTURE", capture)
			t.Setenv("BITS_VERSION", "v0.0.1")
			t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "wrong"))
			t.Setenv("LOCALAPPDATA", filepath.Join(dir, "wrong"))
			t.Setenv("BITS_RELEASES_URL", "https://wrong.invalid")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			requested := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/started" {
					cancel()
					<-r.Context().Done()
					return
				}

				if r.URL.Path == "/releases/latest" {
					http.Redirect(w, r, "/releases/tag/"+tc.latest, http.StatusFound)
					return
				}
				requested = true
				if tc.status != 200 {
					w.WriteHeader(tc.status)
					return
				}
				if tc.interrupted {
					cancel()
					<-r.Context().Done()
					return
				}
				if tc.truncated {
					// A valid, executable prefix must never run after an incomplete transfer.
					hijacker := w.(http.Hijacker)
					conn, buf, err := hijacker.Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer func() {
						if err := conn.Close(); err != nil {
							t.Error(err)
						}
					}()
					if _, err := fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Length: 999999\r\n\r\n"); err != nil {
						t.Error(err)
					}
					if _, err := fmt.Fprintf(buf, "echo unsafe > '%s'\n", capture); err != nil {
						t.Error(err)
					}
					if err := buf.Flush(); err != nil {
						t.Error(err)
					}
					return
				}
				if tc.duringRun {
					if runtime.GOOS == "windows" {
						_, _ = fmt.Fprint(w, "$client=New-Object Net.WebClient; $client.DownloadString($env:BITS_TEST_RUN_URL)\n")
					} else {
						_, _ = fmt.Fprint(w, "curl -fsS \"$BITS_TEST_RUN_URL\" >/dev/null\n")
					}
					return
				}

				if runtime.GOOS == "windows" {
					_, _ = fmt.Fprintf(w, "param($Root,$Version,$UpdateFrom)\n[IO.File]::WriteAllLines($env:BITS_TEST_CAPTURE, @($Root,$Version,$UpdateFrom,$env:BITS_RELEASES_URL))\nexit %d\n", tc.exit)
				} else {
					_, _ = fmt.Fprintf(w, "printf '%%s\\n' \"$2\" \"$4\" \"$6\" \"$BITS_RELEASES_URL\" > \"$BITS_TEST_CAPTURE\"\nexit %d\n", tc.exit)
				}
			}))
			defer server.Close()
			t.Setenv("BITS_TEST_RUN_URL", server.URL+"/started")
			s := newStandalone(server.URL+"/releases", dir, "v1.2.3")
			s.installer = server.URL + "/installer"
			var out bytes.Buffer
			err := s.Upgrade(ctx, nil, &out, &out)
			if tc.status == 200 && !tc.interrupted && !tc.truncated && !tc.duringRun && tc.exit == 0 || tc.status == 0 {
				if err != nil {
					t.Fatalf("Upgrade: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Upgrade = %v, want %q", err, tc.want)
			}
			if tc.status == 0 {
				if requested || !strings.Contains(out.String(), tc.want) {
					t.Fatalf("unexpected installer fetch or output: %t %s", requested, &out)
				}
			}
			data, readErr := os.ReadFile(capture)
			if tc.status == 200 && !tc.interrupted && !tc.truncated && !tc.duringRun {
				want := strings.Join([]string{dir, tc.latest, "v1.2.3", server.URL + "/releases", ""}, "\n")
				if readErr != nil || strings.ReplaceAll(string(data), "\r\n", "\n") != want {
					t.Fatalf("invocation = %q, %v; want %q", data, readErr, want)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("installer ran or capture read failed: %q, %v", data, readErr)
			}
		})
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
	defer server.Close()
	s := newStandalone(server.URL+"/releases", "", "")
	s.check.Transport = server.Client().Transport
	s.download.Transport = server.Client().Transport
	if _, err := s.LatestVersion(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Fatalf("LatestVersion = %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.download.Do(req)
	if resp != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Fatalf("installer download = %v", err)
	}
}

func TestDetectStandalone(t *testing.T) {
	for _, tc := range []struct {
		name         string
		version      string
		buildVersion string
		complete     bool
		want         bool
	}{
		{name: "complete", version: "v1.2.3", buildVersion: "v1.2.3", complete: true, want: true},
		{name: "executable path already points to another release", version: "v1.2.4", buildVersion: "v1.2.3", complete: true, want: true},
		{name: "development binary in released layout", version: "v1.2.3", buildVersion: "dev", complete: true},
		{name: "incomplete", version: "v1.2.4", buildVersion: "v1.2.4"},
		{name: "development layout", version: "dev", buildVersion: "v1.2.3", complete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "versions", tc.version)
			executable := filepath.Join(dir, "bin", executableName)
			if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, nil, 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.complete {
				if err := os.WriteFile(filepath.Join(dir, ".install-complete"), nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := detectBackend(executable, tc.buildVersion)
			if !tc.want {
				if got != nil || !errors.Is(err, ErrUnsupportedInstallation) {
					t.Fatalf("detect = %#v, %v", got, err)
				}
				return
			}
			s, ok := got.(standalone)
			if err != nil || !ok || s.root != root || s.current != tc.buildVersion {
				t.Fatalf("detect = %#v, %v", got, err)
			}
		})
	}
}

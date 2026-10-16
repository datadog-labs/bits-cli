package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type fakeBackend func(context.Context) (string, error)

func (fakeBackend) Name() string                                        { return "homebrew" }
func (f fakeBackend) LatestVersion(ctx context.Context) (string, error) { return f(ctx) }

func testChecker(t *testing.T, b backend) *Checker {
	t.Helper()
	return &Checker{backend: b, path: filepath.Join(t.TempDir(), "update.json")}
}

func testHomebrew(t *testing.T, handler http.HandlerFunc) homebrew {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return newHomebrew(server.URL)
}

func writeState(t *testing.T, c *Checker, s state) {
	t.Helper()
	if err := c.save(s); err != nil {
		t.Fatal(err)
	}
}

func TestCachedDecision(t *testing.T) {
	for _, tc := range []struct{ name, current, latest, excluded, want string }{
		{"newer", "v1.2.3", "v1.2.4", "", "v1.2.4"},
		{"same", "v1.2.3", "v1.2.3", "", ""},
		{"older", "v2.0.0", "v1.99.99", "", ""},
		{"excluded", "v1.2.3", "v1.2.4", "v1.2.4", ""},
		{"next after exclusion", "v1.2.3", "v1.2.5", "v1.2.4", "v1.2.5"},
		{"development", "dev-abcdef", "", "", ""},
		{"prerelease", "v1.2.3-rc.1", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				c := testChecker(t, fakeBackend(func(context.Context) (string, error) {
					calls++
					return "v1.3.0", nil
				}))
				writeState(t, c, state{Backend: "homebrew", LatestVersion: tc.latest, ExcludedVersion: tc.excluded, CheckedAt: time.Now()})
				if got := c.Check(context.Background(), tc.current); got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
				synctest.Wait()
				if calls != 0 {
					t.Fatal("fresh cache or non-stable build started a refresh")
				}
			})
		})
	}
}

func TestCheckDoesNotWaitForNetwork(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		calls := 0
		c := testChecker(t, fakeBackend(func(context.Context) (string, error) {
			calls++
			<-release
			return "v1.3.0", nil
		}))
		defer close(release)
		writeState(t, c, state{Backend: "homebrew", LatestVersion: "v1.2.4", CheckedAt: time.Now().Add(-2 * ttl)})
		result := make(chan string, 1)
		go func() { result <- c.Check(context.Background(), "v1.2.3") }()
		synctest.Wait()
		select {
		case got := <-result:
			if got != "v1.2.4" {
				t.Fatalf("cached result = %q", got)
			}
		default:
			t.Fatal("Check blocked on backend")
		}
		c.Check(context.Background(), "v1.2.3")
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("made %d backend calls", calls)
		}
		release <- struct{}{}
		synctest.Wait()
		if got := c.Check(context.Background(), "v1.2.3"); got != "v1.3.0" {
			t.Fatalf("refreshed result = %q", got)
		}
	})
}

func TestRefreshFailuresPreserveState(t *testing.T) {
	backendErr := errors.New("backend unavailable")
	for _, tc := range []struct {
		name, latest string
		err, wantErr error
	}{
		{"backend error", "", backendErr, backendErr},
		{"canceled", "", context.Canceled, context.Canceled},
		{"missing version", "", nil, ErrInvalidVersion},
		{"prerelease", "v1.3.0-rc.1", nil, ErrInvalidVersion},
		{"abbreviated", "v1.3", nil, ErrInvalidVersion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testChecker(t, fakeBackend(func(context.Context) (string, error) { return tc.latest, tc.err }))
			original := state{Backend: "homebrew", LatestVersion: "v1.2.4", ExcludedVersion: "v1.2.4", CheckedAt: time.Now().UTC().Add(-2 * ttl)}
			writeState(t, c, original)
			if err := c.refresh(context.Background()); !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
			if got := c.load(); got != original {
				t.Fatalf("state changed: %+v", got)
			}
		})
	}
}

func TestRefreshPreservesExclusion(t *testing.T) {
	var c *Checker
	c = testChecker(t, fakeBackend(func(context.Context) (string, error) {
		// Save the exclusion while the refresh is in progress.
		return "v1.3.0", c.Exclude("v1.3.0")
	}))
	if err := c.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Check(context.Background(), "v1.2.3"); got != "" {
		t.Fatalf("excluded version = %q", got)
	}
	if err := c.Exclude("dev"); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("invalid exclusion: %v", err)
	}
	if c.load().ExcludedVersion != "v1.3.0" {
		t.Fatal("invalid exclusion changed state")
	}
	if err := c.Exclude(""); err != nil {
		t.Fatal(err)
	}
	if got := c.Check(context.Background(), "v1.2.3"); got != "v1.3.0" {
		t.Fatalf("cleared exclusion = %q", got)
	}
}

func TestHomebrewFetch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
		invalid    bool
	}{
		{"success", 200, `  version "1.3.0"`, "v1.3.0", false},
		{"server", 500, "", "", false},
		{"missing", 404, "", "", false},
		{"redirect", 302, "", "", false},
		{"malformed", 200, "{", "", true},
		{"oversized", 200, strings.Repeat(" ", 64*1024) + `  version "1.3.0"`, "", false},
		{"trailing garbage", 200, `  version "1.3.0"oops`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			b := testHomebrew(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			got, err := b.LatestVersion(context.Background())
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if errors.Is(err, ErrInvalidVersion) != tc.invalid {
				t.Fatalf("invalid version classification: %v", err)
			}
			if tc.status != 200 && !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tc.status)) {
				t.Fatalf("lost HTTP status: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("made %d HTTP calls", calls.Load())
			}
		})
	}
}

func TestHomebrewCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := testHomebrew(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	})
	if _, err := b.LatestVersion(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
}

func TestStateRecoveryAndTTL(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"absent", ""}, {"corrupt", "{"}, {"other backend", `{"backend":"other","latest_version":"v1.2.4"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := testChecker(t, fakeBackend(func(context.Context) (string, error) { return "v1.3.0", nil }))
				if tc.raw != "" {
					if err := os.WriteFile(c.path, []byte(tc.raw), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if got := c.Check(context.Background(), "v1.2.3"); got != "" {
					t.Fatalf("invalid state produced %q", got)
				}
				synctest.Wait()
				s := c.load()
				if s.LatestVersion != "v1.3.0" || s.stale(time.Now()) {
					t.Fatalf("invalid refreshed state: %+v", s)
				}
			})
		})
	}
	now := time.Now()
	for _, age := range []time.Duration{-time.Second, ttl, 2 * ttl} {
		if !(state{LatestVersion: "v1.2.3", CheckedAt: now.Add(-age)}).stale(now) {
			t.Fatalf("age %s should refresh", age)
		}
	}
	if (state{LatestVersion: "v1.2.3", CheckedAt: now.Add(-ttl + time.Second)}).stale(now) {
		t.Fatal("fresh state expired")
	}
}

func TestDetectBackend(t *testing.T) {
	for _, tc := range []struct {
		name, layout, receipt string
		want                  bool
	}{
		{"homebrew", "Cellar/bits/1.2.3/bin/bits", `{"source":{"tap":"datadog-labs/pack"}}`, true},
		{"revision", "Cellar/bits/1.2.3_1/bin/bits", `{"source":{"tap":"datadog-labs/pack"}}`, true},
		{"other tap", "Cellar/bits/1.2.3/bin/bits", `{"source":{"tap":"someone/else"}}`, false},
		{"missing receipt", "Cellar/bits/1.2.3/bin/bits", "", false},
		{"broken receipt", "Cellar/bits/1.2.3/bin/bits", "{", false},
		{"copied binary", "local/bin/bits", `{"source":{"tap":"datadog-labs/pack"}}`, false},
		{"other formula", "Cellar/other/1.2.3/bin/bits", `{"source":{"tap":"datadog-labs/pack"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, tc.layout)
			if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(executable, nil, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.receipt != "" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(executable)), "INSTALL_RECEIPT.json"), []byte(tc.receipt), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(dir, "bits")
			if err := os.Symlink(executable, link); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{executable, link} {
				got, err := detectBackend(path)
				if tc.want {
					if err != nil || got == nil || got.Name() != "homebrew" {
						t.Fatalf("detect %s = %v, %v", path, got, err)
					}
				} else if got != nil || !errors.Is(err, ErrUnsupportedInstallation) {
					t.Fatalf("detect %s = %v, %v; want unsupported installation", path, got, err)
				}
				if tc.name == "missing receipt" && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing receipt lost filesystem error: %v", err)
				}
			}
		})
	}
}

func TestHomebrewVersion(t *testing.T) {
	for _, tc := range []struct{ name, formula, want string }{
		{"generated format", "class Bits < Formula\n  version \"1.2.3\"\n  on_macos do\n    url \"https://example.com/v9.9.9/bits.tar.gz\"\n  end\nend\n", "v1.2.3"},
		{"windows newlines", "  version \"1.2.3\"\r\n", "v1.2.3"},
		{"missing", "class Bits < Formula\nend\n", ""},
		{"comment", "  # version \"1.2.3\"\n", ""},
		{"duplicate", "  version \"1.2.3\"\n  version \"1.2.4\"\n", ""},
		{"expression", "  version ENV.fetch(\"VERSION\")\n", ""},
		{"interpolation", "  version \"#{version}\"\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := homebrewVersion([]byte(tc.formula))
			if got != tc.want || errors.Is(err, ErrInvalidVersion) != (tc.want == "") {
				t.Fatalf("homebrewVersion = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const conversationsPath = "/api/v2/assistant/user-conversations"

const conversationsBody = `{"data":{"type":"user-conversations-response","attributes":{}}}`

func captureServer(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(conversationsBody))
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func redirectServer(t *testing.T, target string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func apiKeyClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewAPIKeyClient(baseURL, "api", "app")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSameHostRedirectKeepsAPIKeyHeaders(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	t.Cleanup(srv.Close)
	mux := srv.Config.Handler.(*http.ServeMux)
	var mu sync.Mutex
	var got http.Header
	mux.HandleFunc(conversationsPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(conversationsBody))
	})

	if _, err := apiKeyClient(t, srv.URL).UserConversations(context.Background()); err != nil {
		t.Fatalf("same-host redirect failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Get("DD-API-KEY") != "api" || got.Get("DD-APPLICATION-KEY") != "app" {
		t.Fatalf("same-host redirect dropped API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			got.Get("DD-API-KEY"), got.Get("DD-APPLICATION-KEY"))
	}
}

// A single httptest port cannot serve both https and http, so drive checkRedirect directly.
func TestSameHostSchemeDowngradeRedirectDropsAPIKeyHeaders(t *testing.T) {
	origin := httptest.NewRequest(http.MethodGet, "https://example.test"+conversationsPath, nil)
	redirected := httptest.NewRequest(http.MethodGet, "http://example.test"+conversationsPath, nil)
	redirected.Header.Set("DD-API-KEY", "api")
	redirected.Header.Set("DD-APPLICATION-KEY", "app")

	if err := checkRedirect(redirected, []*http.Request{origin}); err != nil {
		t.Fatalf("same-host scheme downgrade rejected the redirect: %v", err)
	}
	if redirected.Header.Get("DD-API-KEY") != "" || redirected.Header.Get("DD-APPLICATION-KEY") != "" {
		t.Fatalf("same-host scheme downgrade leaked API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			redirected.Header.Get("DD-API-KEY"), redirected.Header.Get("DD-APPLICATION-KEY"))
	}
}

func TestSameOriginRedirectWithDefaultPortKeepsAPIKeyHeaders(t *testing.T) {
	origin := httptest.NewRequest(http.MethodGet, "https://example.test"+conversationsPath, nil)
	redirected := httptest.NewRequest(http.MethodGet, "https://example.test:443"+conversationsPath, nil)
	redirected.Header.Set("DD-API-KEY", "api")
	redirected.Header.Set("DD-APPLICATION-KEY", "app")

	if err := checkRedirect(redirected, []*http.Request{origin}); err != nil {
		t.Fatalf("same-origin redirect with an explicit default port rejected: %v", err)
	}
	if redirected.Header.Get("DD-API-KEY") != "api" || redirected.Header.Get("DD-APPLICATION-KEY") != "app" {
		t.Fatalf("same-origin redirect with an explicit default port dropped API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			redirected.Header.Get("DD-API-KEY"), redirected.Header.Get("DD-APPLICATION-KEY"))
	}
}

func TestSameHostNonDefaultPortRedirectDropsAPIKeyHeaders(t *testing.T) {
	origin := httptest.NewRequest(http.MethodGet, "https://example.test"+conversationsPath, nil)
	redirected := httptest.NewRequest(http.MethodGet, "https://example.test:8443"+conversationsPath, nil)
	redirected.Header.Set("DD-API-KEY", "api")
	redirected.Header.Set("DD-APPLICATION-KEY", "app")

	if err := checkRedirect(redirected, []*http.Request{origin}); err != nil {
		t.Fatalf("same-host port change rejected the redirect: %v", err)
	}
	if redirected.Header.Get("DD-API-KEY") != "" || redirected.Header.Get("DD-APPLICATION-KEY") != "" {
		t.Fatalf("same-host port change leaked API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			redirected.Header.Get("DD-API-KEY"), redirected.Header.Get("DD-APPLICATION-KEY"))
	}
}

func TestCrossHostRedirectDropsAPIKeyHeaders(t *testing.T) {
	target, headers := captureServer(t)
	origin := redirectServer(t, target.URL+"/final")

	if _, err := apiKeyClient(t, origin.URL).UserConversations(context.Background()); err != nil {
		t.Fatalf("cross-host redirect failed: %v", err)
	}
	got := headers()
	if got.Get("DD-API-KEY") != "" || got.Get("DD-APPLICATION-KEY") != "" {
		t.Fatalf("cross-host redirect leaked API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			got.Get("DD-API-KEY"), got.Get("DD-APPLICATION-KEY"))
	}
}

func TestCrossHostRedirectDropsAPIKeyHeadersOnFallbackClient(t *testing.T) {
	target, headers := captureServer(t)
	origin := redirectServer(t, target.URL+"/final")

	c := &Client{BaseURL: origin.URL, APIKey: "api", AppKey: "app"}
	if _, err := c.UserConversations(context.Background()); err != nil {
		t.Fatalf("cross-host redirect failed: %v", err)
	}
	got := headers()
	if got.Get("DD-API-KEY") != "" || got.Get("DD-APPLICATION-KEY") != "" {
		t.Fatalf("fallback client leaked API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			got.Get("DD-API-KEY"), got.Get("DD-APPLICATION-KEY"))
	}
}

func TestCrossHostRedirectDropsAPIKeyHeadersOnStreamClient(t *testing.T) {
	target, headers := captureServer(t)
	var mu sync.Mutex
	var originHeaders http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		originHeaders = r.Header.Clone()
		mu.Unlock()
		http.Redirect(w, r, target.URL+"/final", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	// An API-key client actually carries DD-* headers on the streaming POST,
	// unlike an OAuth client, which only sends Authorization.
	if _, err := apiKeyClient(t, origin.URL).Send(context.Background(), "hello", SendOptions{}, nil); err != nil {
		t.Fatalf("stream through a cross-host redirect failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := headers(); got.Get("DD-API-KEY") != "" || got.Get("DD-APPLICATION-KEY") != "" {
		t.Fatalf("stream target leaked API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			got.Get("DD-API-KEY"), got.Get("DD-APPLICATION-KEY"))
	}
	if got := originHeaders; got.Get("DD-API-KEY") != "api" || got.Get("DD-APPLICATION-KEY") != "app" {
		t.Fatalf("origin did not receive the API key headers: DD-API-KEY=%q DD-APPLICATION-KEY=%q",
			got.Get("DD-API-KEY"), got.Get("DD-APPLICATION-KEY"))
	}
}

func TestRedirectAuthorizationBehaviorUnchanged(t *testing.T) {
	t.Run("same domain keeps Authorization while API keys are dropped", func(t *testing.T) {
		target, headers := captureServer(t)
		origin := redirectServer(t, target.URL+"/final")

		c, err := NewOAuthClient(origin.URL, staticAccessToken("secret-token"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.UserConversations(context.Background()); err != nil {
			t.Fatalf("cross-host redirect failed: %v", err)
		}
		got := headers()
		if got.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("Authorization did not follow the stdlib same-domain rule: %q", got.Get("Authorization"))
		}
		if got.Get("DD-API-KEY") != "" || got.Get("DD-APPLICATION-KEY") != "" {
			t.Fatalf("cross-host redirect leaked API key headers")
		}
	})
	t.Run("cross domain drops Authorization as the stdlib does", func(t *testing.T) {
		target, headers := captureServer(t)
		originURL := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
		origin := redirectServer(t, originURL+"/final")

		c, err := NewOAuthClient(origin.URL, staticAccessToken("secret-token"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.UserConversations(context.Background()); err != nil {
			t.Fatalf("cross-domain redirect failed: %v", err)
		}
		got := headers()
		if got.Get("Authorization") != "" {
			t.Fatalf("cross-domain redirect leaked Authorization: %q", got.Get("Authorization"))
		}
		if got.Get("DD-API-KEY") != "" || got.Get("DD-APPLICATION-KEY") != "" {
			t.Fatalf("cross-domain redirect leaked API key headers")
		}
	})
}

func TestRedirectLimitStillApplies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, conversationsPath, http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	_, err := apiKeyClient(t, srv.URL).UserConversations(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("redirect loop error = %v, want the 10-redirect limit", err)
	}
}

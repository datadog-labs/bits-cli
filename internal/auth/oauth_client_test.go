package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// redirectTestPair stands up an OAuth endpoint that answers 307 to a target
// server which records every hit. The target mimics what a redirect-following
// client would hand a secret-bearing token body to.
func redirectTestPair(t *testing.T) (redirect *httptest.Server, targetHits *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"leaked","refresh_token":"leaked-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(target.Close)
	redirect = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	return redirect, &hits
}

// Revoke posts its token in the form body with this exact client shape, so the
// constructor must surface the 3xx instead of replaying the body.
func TestNewOAuthHTTPClientSurfacesRedirectWithoutFollowing(t *testing.T) {
	redirect, targetHits := redirectTestPair(t)

	client := newOAuthHTTPClient(10 * time.Second)
	resp, err := client.PostForm(redirect.URL, url.Values{"token": {"refresh-secret"}})
	if err != nil {
		t.Fatalf("POST via OAuth client: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want the 3xx surfaced as the response", resp.StatusCode)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was hit %d times", targetHits.Load())
	}
	if client.Timeout != 10*time.Second {
		t.Fatalf("timeout = %v, want 10s", client.Timeout)
	}
}

// NewSource's default client construction must refuse redirects.
func TestNewSourceDefaultClientRefusesRedirects(t *testing.T) {
	noStagingEnv(t)
	source, err := NewSource(expiredSession(), newMemoryStore(expiredSession()), nil)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if source.httpClient.CheckRedirect == nil {
		t.Fatal("default background refresh client follows redirects")
	}
	if got := source.httpClient.CheckRedirect(&http.Request{}, nil); got != http.ErrUseLastResponse {
		t.Fatalf("CheckRedirect error = %v, want http.ErrUseLastResponse", got)
	}
	if source.httpClient.Timeout != refreshTimeout {
		t.Fatalf("timeout = %v, want %v", source.httpClient.Timeout, refreshTimeout)
	}
}

// The nil-client defaults in login and Revoke must yield a redirect-refusing
// client with the configured timeout, while an explicit client passes through
// unchanged.
func TestDefaultOAuthClientGuardsNilInput(t *testing.T) {
	client := defaultOAuthClient(nil, loginHTTPTimeout)
	if got := client.CheckRedirect(&http.Request{}, nil); got != http.ErrUseLastResponse {
		t.Fatalf("CheckRedirect error = %v, want http.ErrUseLastResponse", got)
	}
	if client.Timeout != loginHTTPTimeout {
		t.Fatalf("timeout = %v, want %v", client.Timeout, loginHTTPTimeout)
	}

	provided := &http.Client{Timeout: time.Second}
	if got := defaultOAuthClient(provided, revokeHTTPTimeout); got != provided {
		t.Fatalf("defaultOAuthClient returned %p, want the caller's client %p", got, provided)
	}
}

// The login code exchange must error on a redirecting token endpoint instead of
// replaying the authorization code and code_verifier to the redirect target.
// The login default client cannot reach an httptest token endpoint directly
// because the validated callback domain rewrites TokenURL to the production
// API host, so this drives the exchange with the same client the default
// construction site builds, routed through a transport rewrite like the other
// login tests.
func TestLoginExchangeDoesNotFollowTokenRedirect(t *testing.T) {
	redirect, targetHits := redirectTestPair(t)

	client := newOAuthHTTPClient(loginHTTPTimeout)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "api.datadoghq.com" {
			t.Errorf("token exchange host = %s, want api.datadoghq.com", req.URL.Hostname())
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(redirect.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})

	store := &memoryStore{}
	_, err := login(context.Background(), SiteConfig{
		Site:         DefaultSite,
		ClientID:     "client",
		AuthorizeURL: redirect.URL + "/authorize",
		TokenURL:     redirect.URL + "/api/v2/oauth2/token",
		RevokeURL:    redirect.URL + "/oauth2/v1/revoke",
		RedirectURI:  DefaultRedirectURI,
	}, LoginOptions{Store: store, HTTPClient: client, OpenURL: callbackOpenURL(t)})
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("login error = %v, want the surfaced token endpoint redirect", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was hit %d times", targetHits.Load())
	}
	if store.saves != 0 {
		t.Fatalf("store saves = %d, want 0", store.saves)
	}
}

// Background refresh must error on a redirecting token endpoint instead of
// replaying the refresh token to the redirect target. This drives the refresh
// through Source.AccessToken with the same client NewSource installs by
// default, routed to the fake endpoint through a transport rewrite.
func TestSourceRefreshDoesNotFollowTokenRedirect(t *testing.T) {
	noStagingEnv(t)
	redirect, targetHits := redirectTestPair(t)

	initial := expiredSession()
	store := newMemoryStore(initial)
	client := newOAuthHTTPClient(refreshTimeout)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "api.datadoghq.com" {
			t.Errorf("refresh host = %s, want api.datadoghq.com", req.URL.Hostname())
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(redirect.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	source, err := NewSource(initial, store, client)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	token, err := source.AccessToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("AccessToken = %q, %v; want the surfaced token endpoint redirect", token, err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was hit %d times", targetHits.Load())
	}
	if store.saves != 0 || store.session.RefreshToken != "old-refresh" {
		t.Fatalf("stored session = %#v, saves = %d", store.session, store.saves)
	}
}

// Logout's revocation must error on a redirecting revoke endpoint instead of
// replaying the refresh token to the redirect target. Revoke derives its
// endpoints from ConfigForSite, so the production revoke URL is routed to the
// fake endpoint through a transport rewrite on the same client Revoke builds
// by default.
func TestLogoutRevokeDoesNotFollowRedirect(t *testing.T) {
	noStagingEnv(t)
	redirect, targetHits := redirectTestPair(t)

	client := newOAuthHTTPClient(revokeHTTPTimeout)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() != "api.datadoghq.com" {
			t.Errorf("revoke host = %s, want api.datadoghq.com", req.URL.Hostname())
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(redirect.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})

	session := Session{
		Site:         "https://api.datadoghq.com",
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour),
	}
	store := newMemoryStore(session)
	hadSession, revokeErr, err := Logout(context.Background(), store, client)
	if err != nil || !hadSession {
		t.Fatalf("Logout = had %v, err %v", hadSession, err)
	}
	if revokeErr == nil || !strings.Contains(revokeErr.Error(), "HTTP 307") {
		t.Fatalf("revoke error = %v, want the surfaced revoke endpoint redirect", revokeErr)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was hit %d times", targetHits.Load())
	}
	if store.present || store.deletes != 1 {
		t.Fatalf("store present = %v, deletes = %d", store.present, store.deletes)
	}
}

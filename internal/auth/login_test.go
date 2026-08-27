package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestRenderLoginCompleteEscapesHost(t *testing.T) {
	var buf bytes.Buffer
	if err := renderLoginComplete(&buf, "127.0.0.1:1<script>alert(1)</script>"); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Login complete") {
		t.Errorf("missing page body: %q", out)
	}
	if strings.Contains(out, `class="error"`) {
		t.Errorf("success page marked as error: %q", out)
	}
	if strings.Contains(out, "<script>") {
		t.Errorf("host was not escaped: %q", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped host: %q", out)
	}
}

func TestWriteLoginErrorRendersEscapedHTMLPage(t *testing.T) {
	rec := httptest.NewRecorder()
	writeLoginError(rec, http.StatusBadRequest, "boom <script>alert(1)</script>")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Login failed") {
		t.Errorf("missing error heading: %q", body)
	}
	if !strings.Contains(body, `class="error"`) {
		t.Errorf("error page missing error class: %q", body)
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("message not escaped: %q", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected escaped message: %q", body)
	}
}

func TestLoginCompletesPKCEExchangeAndStoresSession(t *testing.T) {
	var redirectURI string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			t.Errorf("token path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "auth-code" {
			t.Errorf("exchange form = %v", r.Form)
		}
		if r.Form.Get("client_id") != "client" || r.Form.Get("code_verifier") == "" || r.Form.Get("redirect_uri") != redirectURI {
			t.Errorf("exchange form = %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer issuer.Close()

	cfg := SiteConfig{
		Site:         DefaultStagingSite,
		ClientID:     "client",
		AuthorizeURL: issuer.URL + "/authorize",
		TokenURL:     issuer.URL + "/token",
		RevokeURL:    issuer.URL + "/revoke",
		RedirectURI:  DefaultRedirectURI,
	}
	httpClient := issuer.Client()
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.datad0g.com" || req.URL.Path != "/oauth2/v1/token" {
			t.Errorf("token exchange URL = %s", req.URL)
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		clone.URL.Path = "/token"
		return http.DefaultTransport.RoundTrip(clone)
	})

	store := &memoryStore{}
	var output bytes.Buffer
	openURL := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		query := u.Query()
		if query.Get("scope") != "" || query.Get("code_challenge") == "" || query.Get("code_challenge_method") != "S256" {
			t.Errorf("authorization query = %v", query)
		}
		redirectURI = query.Get("redirect_uri")
		callbackURL, callbackErr := url.Parse(redirectURI)
		if callbackErr != nil || callbackURL.Hostname() != "127.0.0.1" || callbackURL.Port() == "" || callbackURL.Port() == "0" {
			t.Fatalf("dynamic redirect URI = %q, parse error = %v", redirectURI, callbackErr)
		}
		go func() {
			callback := redirectURI + "?code=auth-code&domain=datad0g.com&state=" + url.QueryEscape(query.Get("state"))
			resp, callbackErr := http.Get(callback) //nolint:gosec // loopback test callback
			if callbackErr != nil {
				t.Errorf("callback: %v", callbackErr)
				return
			}
			_ = resp.Body.Close()
		}()
		return nil
	}

	var reportedURL string
	session, err := login(context.Background(), cfg, LoginOptions{
		Store:      store,
		HTTPClient: httpClient,
		OpenURL:    openURL,
		OnBrowserOpen: func(raw string, openErr error) {
			reportedURL = raw
			if openErr != nil {
				t.Errorf("reported browser error = %v", openErr)
			}
		},
		Out: &output,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if session.AccessToken != "access" || session.RefreshToken != "refresh" || session.Site != "https://api.datad0g.com" {
		t.Errorf("session = %#v", session)
	}
	if store.saves != 1 || store.session.AccessToken != "access" {
		t.Errorf("stored session = %#v, saves = %d", store.session, store.saves)
	}
	if !strings.Contains(output.String(), "Datadog OAuth callback domain: datad0g.com") {
		t.Errorf("output did not report callback domain: %q", output.String())
	}
	if reportedURL == "" || !strings.Contains(reportedURL, "code_challenge=") {
		t.Errorf("reported browser URL = %q", reportedURL)
	}
}

func TestOpenBrowserHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := openBrowser(ctx, "https://app.datadoghq.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("openBrowser error = %v, want context cancellation", err)
	}
}

func TestCallbackChoosesEphemeralPortAndIgnoresWrongState(t *testing.T) {
	listener, results, err := listenForCallback(DefaultRedirectURI, "expected")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = listener.server.Shutdown(ctx)
	}()

	redirectURL, err := url.Parse(listener.redirectURI)
	if err != nil {
		t.Fatal(err)
	}
	if redirectURL.Hostname() != "127.0.0.1" || redirectURL.Port() == "" || redirectURL.Port() == "0" {
		t.Fatalf("selected redirect URI = %q", listener.redirectURI)
	}

	resp, err := http.Get(listener.redirectURI + "?code=wrong&state=unexpected") //nolint:gosec // loopback test callback
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong-state status = %d", resp.StatusCode)
	}
	select {
	case result := <-results:
		t.Fatalf("wrong state ended login: %#v", result)
	default:
	}

	resp, err = http.Get(listener.redirectURI + "?code=right&state=expected") //nolint:gosec // loopback test callback
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	result := <-results
	if result.err != nil || result.code != "right" {
		t.Fatalf("callback result = %#v", result)
	}
}

func TestCallbackSanitizesAuthorizationErrorDescription(t *testing.T) {
	listener, results, err := listenForCallback(DefaultRedirectURI, "expected")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = listener.server.Shutdown(ctx)
	}()
	resp, err := http.Get(listener.redirectURI + "?error=access_denied&error_description=do-not-leak-secret&state=expected") //nolint:gosec // loopback test callback
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	result := <-results
	if result.err == nil || !strings.Contains(result.err.Error(), "access_denied") || strings.Contains(result.err.Error(), "do-not-leak-secret") {
		t.Fatalf("callback error = %v", result.err)
	}
}

func TestTokenErrorSanitizerOmitsResponseBody(t *testing.T) {
	err := &oauth2.RetrieveError{
		Response:         &http.Response{StatusCode: http.StatusBadRequest},
		ErrorCode:        "invalid_grant",
		ErrorDescription: "do-not-leak-secret",
		Body:             []byte(`{"error":"invalid_grant","secret":"do-not-leak-secret"}`),
	}
	got := sanitizedOAuthError("exchange Datadog OAuth code", err)
	if !strings.Contains(got.Error(), "HTTP 400 (invalid_grant)") || strings.Contains(got.Error(), "do-not-leak-secret") {
		t.Fatalf("sanitized error = %v", got)
	}
}

func TestCallbackRejectsNonLiteralAndMalformedRedirects(t *testing.T) {
	for _, redirectURI := range []string{
		"http://localhost:0/oauth/callback",
		"http://0.0.0.0:0/oauth/callback",
		"http://127.0.0.1:0",
		"http://127.0.0.1:0/oauth/callback?unexpected=true",
		"https://127.0.0.1:0/oauth/callback",
	} {
		t.Run(redirectURI, func(t *testing.T) {
			if _, _, err := listenForCallback(redirectURI, "state"); err == nil {
				t.Fatalf("listenForCallback(%q) succeeded", redirectURI)
			}
		})
	}
}

func TestLoginRevokesUnpersistedGrant(t *testing.T) {
	var revokedToken string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/v1/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
		case "/oauth2/v1/revoke":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			revokedToken = r.Form.Get("token")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer issuer.Close()

	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	store := &memoryStore{saveFailures: -1, saveErr: fmt.Errorf("keyring unavailable")}
	openURL := callbackOpenURL(t)
	_, err := login(context.Background(), SiteConfig{
		Site: DefaultStagingSite, ClientID: "client", AuthorizeURL: issuer.URL + "/authorize",
		TokenURL: issuer.URL + "/oauth2/v1/token", RevokeURL: issuer.URL + "/oauth2/v1/revoke", RedirectURI: DefaultRedirectURI,
	}, LoginOptions{Store: store, HTTPClient: client, OpenURL: openURL})
	if err == nil || !strings.Contains(err.Error(), "persist new OAuth session") {
		t.Fatalf("login error = %v", err)
	}
	if strings.Contains(err.Error(), "new-refresh") || revokedToken != "new-refresh" {
		t.Fatalf("revoked token = %q, error = %v", revokedToken, err)
	}
}

func TestReplacementLoginCommitsThenRevokesPreviousGrant(t *testing.T) {
	previous := Session{
		Site: "https://api.datad0g.com", ClientID: "client", AccessToken: "old-access",
		RefreshToken: "old-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour),
	}
	store := newMemoryStore(previous)
	store.afterLockErr = fmt.Errorf("%w: injected", ErrSessionUnlock)
	var revokedToken string
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/v1/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
		case "/oauth2/v1/revoke":
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			revokedToken = r.Form.Get("token")
			stored, loadErr := store.Load()
			if loadErr != nil || stored.AccessToken != "new-access" {
				t.Errorf("replacement was not durable before revocation: %#v, %v", stored, loadErr)
			}
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	_, err := login(context.Background(), SiteConfig{
		Site: DefaultStagingSite, ClientID: "client", AuthorizeURL: issuer.URL + "/authorize",
		TokenURL: issuer.URL + "/oauth2/v1/token", RevokeURL: issuer.URL + "/oauth2/v1/revoke", RedirectURI: DefaultRedirectURI,
	}, LoginOptions{Store: store, HTTPClient: client, OpenURL: callbackOpenURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	if revokedToken != "old-refresh" || store.session.RefreshToken != "new-refresh" {
		t.Fatalf("revoked = %q, stored = %#v", revokedToken, store.session)
	}
}

func TestReplacementLoginDoesNotRevokeSharedRefreshToken(t *testing.T) {
	previous := Session{
		Site: "https://api.datad0g.com", ClientID: "client", AccessToken: "old-access",
		RefreshToken: "shared-refresh", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour),
	}
	store := newMemoryStore(previous)
	var revokeCalls int
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/v1/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"shared-refresh","token_type":"Bearer","expires_in":3600}`)
		case "/oauth2/v1/revoke":
			revokeCalls++
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})

	session, err := login(context.Background(), SiteConfig{
		Site: DefaultStagingSite, ClientID: "client", AuthorizeURL: issuer.URL + "/authorize",
		TokenURL: issuer.URL + "/oauth2/v1/token", RevokeURL: issuer.URL + "/oauth2/v1/revoke", RedirectURI: DefaultRedirectURI,
	}, LoginOptions{Store: store, HTTPClient: client, OpenURL: callbackOpenURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	if revokeCalls != 0 || session.AccessToken != "new-access" || store.session.RefreshToken != "shared-refresh" {
		t.Fatalf("revoke calls = %d, session = %#v, stored = %#v", revokeCalls, session, store.session)
	}
}

func TestSessionRevocationCredentialPrefersRefreshToken(t *testing.T) {
	withRefresh := sessionRevocationCredential(Session{AccessToken: "access", RefreshToken: "refresh"})
	if withRefresh != (revocationCredential{token: "refresh", hint: "refresh_token"}) {
		t.Fatalf("with refresh token = %#v", withRefresh)
	}
	withoutRefresh := sessionRevocationCredential(Session{AccessToken: "access"})
	if withoutRefresh != (revocationCredential{token: "access", hint: "access_token"}) {
		t.Fatalf("without refresh token = %#v", withoutRefresh)
	}
}

func callbackOpenURL(t *testing.T) func(string) error {
	t.Helper()
	return func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		query := u.Query()
		go func() {
			callback := query.Get("redirect_uri") + "?code=auth-code&domain=datad0g.com&state=" + url.QueryEscape(query.Get("state"))
			resp, callbackErr := http.Get(callback) //nolint:gosec // loopback test callback
			if callbackErr != nil {
				t.Errorf("callback: %v", callbackErr)
				return
			}
			_ = resp.Body.Close()
		}()
		return nil
	}
}

func TestLoginReplacesCorruptCredential(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/v1/token" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`)
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	store := &memoryStore{
		present: true,
		loadErr: fmt.Errorf("%w: truncated credential", ErrSessionCorrupt),
	}
	session, err := login(context.Background(), SiteConfig{
		Site: DefaultStagingSite, ClientID: "client", AuthorizeURL: issuer.URL + "/authorize",
		TokenURL: issuer.URL + "/oauth2/v1/token", RevokeURL: issuer.URL + "/oauth2/v1/revoke", RedirectURI: DefaultRedirectURI,
	}, LoginOptions{Store: store, HTTPClient: client, OpenURL: callbackOpenURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	if session.AccessToken != "new-access" || store.session.AccessToken != "new-access" || store.saves != 1 {
		t.Fatalf("session = %#v, stored = %#v", session, store.session)
	}
}

func TestLogoutClearsCorruptCredential(t *testing.T) {
	store := &memoryStore{
		present: true,
		loadErr: fmt.Errorf("%w: truncated credential", ErrSessionCorrupt),
	}
	hadSession, revokeErr, err := Logout(context.Background(), store, nil)
	if err != nil || revokeErr != nil || !hadSession {
		t.Fatalf("Logout = had %v, revoke %v, err %v", hadSession, revokeErr, err)
	}
	if store.present || store.deletes != 1 {
		t.Fatalf("present = %v, deletes = %d", store.present, store.deletes)
	}
}

func TestLogoutRevokesDistinctSessionsFromBothStoreBackends(t *testing.T) {
	store := newTestStore(t, true)
	keyringSession := fileTestSession()
	keyringSession.AccessToken = "keyring-access"
	keyringSession.RefreshToken = "keyring-refresh"
	fileSession := fileTestSession()
	fileSession.AccessToken = "file-access"
	fileSession.RefreshToken = "file-refresh"
	if err := keyringSave(keyringSession); err != nil {
		t.Fatalf("seed keyring session: %v", err)
	}
	if err := saveSessionFile(store.filePath, false, fileSession); err != nil {
		t.Fatalf("seed file session: %v", err)
	}

	revoked := make(chan string, 2)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/v1/revoke" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		revoked <- r.Form.Get("token")
		w.WriteHeader(http.StatusOK)
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})

	hadSession, revokeErr, err := Logout(context.Background(), store, client)
	if err != nil || revokeErr != nil || !hadSession {
		t.Fatalf("Logout = had %v, revoke %v, err %v", hadSession, revokeErr, err)
	}
	got := map[string]bool{<-revoked: true, <-revoked: true}
	if !got[keyringSession.RefreshToken] || !got[fileSession.RefreshToken] {
		t.Fatalf("revoked tokens = %v", got)
	}
	if _, err := keyringLoad(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("keyring session survived logout: %v", err)
	}
	if _, err := os.Stat(store.filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file session survived logout: %v", err)
	}
}

func TestRevokeUsesRefreshToken(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("client_id") != "client" || r.Form.Get("token") != "refresh" || r.Form.Get("token_type_hint") != "refresh_token" {
			t.Errorf("revoke form = %v", r.Form)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Errorf("Authorization = %q, want Bearer access", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer issuer.Close()

	// Revoke uses ConfigForSite, so route its transport to the fake issuer while
	// preserving the production request URL generated from the session.
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	err := Revoke(context.Background(), Session{
		Site:         DefaultStagingSite,
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
	}, client)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
}

func TestRevokeRefreshesExpiredAccessTokenBeforeRevoking(t *testing.T) {
	var refreshed, revoked bool
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/oauth2/v1/token":
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "old-refresh" {
				t.Errorf("refresh form = %v", r.Form)
			}
			refreshed = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"access_token":"fresh-access","refresh_token":"fresh-refresh","token_type":"Bearer","expires_in":3600}`)
		case "/oauth2/v1/revoke":
			if got := r.Header.Get("Authorization"); got != "Bearer fresh-access" {
				t.Errorf("Authorization = %q, want Bearer fresh-access", got)
			}
			if r.Form.Get("token") != "fresh-refresh" {
				t.Errorf("revoke token = %q, want fresh-refresh", r.Form.Get("token"))
			}
			revoked = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	err := Revoke(context.Background(), Session{
		Site: DefaultStagingSite, ClientID: "client",
		AccessToken: "expired-access", RefreshToken: "old-refresh",
		Expiry: time.Now().Add(-time.Hour),
	}, client)
	if err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if !refreshed || !revoked {
		t.Fatalf("refreshed=%v revoked=%v", refreshed, revoked)
	}
}

func TestRevokeTreatsInvalidGrantAsAlreadyRevoked(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/v1/token" {
			t.Errorf("revoke should not be reached; path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	err := Revoke(context.Background(), Session{
		Site: DefaultStagingSite, ClientID: "client",
		AccessToken: "expired-access", RefreshToken: "dead-refresh",
		Expiry: time.Now().Add(-time.Hour),
	}, client)
	if err != nil {
		t.Fatalf("Revoke should treat invalid_grant as already revoked: %v", err)
	}
}

func TestRevokeSanitizesAuthorizationServerError(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":"invalid_request","error_description":"do not leak refresh-secret"}`)
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	err := Revoke(context.Background(), Session{
		Site: DefaultStagingSite, ClientID: "client", AccessToken: "access", RefreshToken: "refresh-secret",
	}, client)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400 (invalid_request)") || strings.Contains(err.Error(), "refresh-secret") {
		t.Fatalf("Revoke error = %v", err)
	}
}

func TestLogoutDeletesBeforeBestEffortRevocation(t *testing.T) {
	session := Session{
		Site: DefaultStagingSite, ClientID: "client", AccessToken: "access", RefreshToken: "refresh",
	}
	store := newMemoryStore(session)
	store.afterLockErr = fmt.Errorf("%w: injected", ErrSessionUnlock)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, loadErr := store.Load(); !errors.Is(loadErr, ErrNoSession) {
			t.Errorf("session was still durable during revocation: %v", loadErr)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer issuer.Close()
	client := issuer.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = strings.TrimPrefix(issuer.URL, "http://")
		return http.DefaultTransport.RoundTrip(clone)
	})
	hadSession, revokeErr, err := Logout(context.Background(), store, client)
	if err != nil || !hadSession || revokeErr == nil {
		t.Fatalf("Logout = had %v, revoke %v, err %v", hadSession, revokeErr, err)
	}
	if store.present || store.deletes != 1 {
		t.Fatalf("store present = %v, deletes = %d", store.present, store.deletes)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

package auth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

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

	session, err := login(context.Background(), cfg, LoginOptions{
		Store:      store,
		HTTPClient: httpClient,
		OpenURL:    openURL,
		Out:        &output,
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

func TestCallbackRejectsNonLiteralAndMalformedRedirects(t *testing.T) {
	for _, redirectURI := range []string{
		"http://localhost:0/step2",
		"http://0.0.0.0:0/step2",
		"http://127.0.0.1:0",
		"http://127.0.0.1:0/step2?unexpected=true",
		"https://127.0.0.1:0/step2",
	} {
		t.Run(redirectURI, func(t *testing.T) {
			if _, _, err := listenForCallback(redirectURI, "state"); err == nil {
				t.Fatalf("listenForCallback(%q) succeeded", redirectURI)
			}
		})
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

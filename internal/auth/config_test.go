package auth

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestConfigForSite_Staging(t *testing.T) {
	cfg, err := ConfigForSite("dd.datad0g.com", "")
	if err != nil {
		t.Fatalf("ConfigForSite: %v", err)
	}
	if cfg.Site != "https://dd.datad0g.com" {
		t.Errorf("Site = %q", cfg.Site)
	}
	if cfg.ClientID != StagingClientID {
		t.Errorf("ClientID = %q", cfg.ClientID)
	}
	if cfg.AuthorizeURL != "https://dd.datad0g.com/oauth2/v1/authorize" {
		t.Errorf("AuthorizeURL = %q", cfg.AuthorizeURL)
	}
	if cfg.TokenURL != "https://api.datad0g.com/oauth2/v1/token" {
		t.Errorf("TokenURL = %q", cfg.TokenURL)
	}
}

func TestConfigForSite_ProductionRequiresClient(t *testing.T) {
	_, err := ConfigForSite("https://us3.datadoghq.com", "")
	if err == nil || !strings.Contains(err.Error(), "BITS_OAUTH_CLIENT_ID") {
		t.Fatalf("error = %v, want missing production client", err)
	}
	cfg, err := ConfigForSite("https://us3.datadoghq.com", "production-id")
	if err != nil {
		t.Fatalf("ConfigForSite with override: %v", err)
	}
	if cfg.AuthorizeURL != "https://us3.datadoghq.com/oauth2/v1/authorize" || cfg.TokenURL != "https://api.us3.datadoghq.com/oauth2/v1/token" {
		t.Errorf("unexpected regional endpoints: %#v", cfg)
	}
}

func TestAuthorizationURL_UsesPKCEWithoutExplicitScope(t *testing.T) {
	cfg, err := ConfigForSite(DefaultStagingSite, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := cfg.OAuth2Config().AuthCodeURL("state", oauth2.S256ChallengeOption("challenge"))
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	if query.Get("client_id") != StagingClientID || query.Get("state") != "state" {
		t.Errorf("query = %v", query)
	}
	if query.Get("code_challenge") != oauth2.S256ChallengeFromVerifier("challenge") || query.Get("code_challenge_method") != "S256" {
		t.Errorf("PKCE query = %v", query)
	}
	if _, present := query["scope"]; present {
		t.Errorf("scope must be omitted, query = %v", query)
	}
}

func TestNormalizeSiteRejectsUnsafeInput(t *testing.T) {
	for _, raw := range []string{
		"http://dd.datad0g.com",
		"https://user@example.com",
		"https://dd.datad0g.com/path",
		"https://dd.datad0g.com?x=1",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, _, err := normalizeSite(raw); err == nil {
				t.Fatalf("normalizeSite(%q) succeeded", raw)
			}
		})
	}
}

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
	if cfg.AssistantBase != "https://api.datad0g.com" {
		t.Errorf("AssistantBase = %q", cfg.AssistantBase)
	}
	if cfg.RedirectURI != "http://127.0.0.1:0/oauth/callback" {
		t.Errorf("RedirectURI = %q", cfg.RedirectURI)
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

func TestConfigForSite_RegionalCustomSubdomain(t *testing.T) {
	cfg, err := ConfigForSite("https://acme.us3.datadoghq.com", "production-id")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthorizeURL != "https://us3.datadoghq.com/oauth2/v1/authorize" {
		t.Errorf("AuthorizeURL = %q", cfg.AuthorizeURL)
	}
}

func TestWithCallbackDomain_UsesCanonicalRegionalAPIHost(t *testing.T) {
	for _, domain := range []string{
		"datad0g.com",
		"datadoghq.com",
		"datadoghq.eu",
		"us3.datadoghq.com",
		"us5.datadoghq.com",
		"ap1.datadoghq.com",
		"ap2.datadoghq.com",
	} {
		t.Run(domain, func(t *testing.T) {
			initial, err := ConfigForSite(DefaultStagingSite, "")
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := initial.WithCallbackDomain("  " + strings.ToUpper(domain) + "  ")
			if err != nil {
				t.Fatal(err)
			}
			apiBase := "https://api." + domain
			if cfg.Site != apiBase || cfg.AssistantBase != apiBase {
				t.Errorf("routing = Site %q, AssistantBase %q; want %q", cfg.Site, cfg.AssistantBase, apiBase)
			}
			if cfg.TokenURL != apiBase+"/oauth2/v1/token" || cfg.RevokeURL != apiBase+"/oauth2/v1/revoke" {
				t.Errorf("OAuth endpoints = token %q, revoke %q", cfg.TokenURL, cfg.RevokeURL)
			}
			if cfg.AuthorizeURL != initial.AuthorizeURL {
				t.Errorf("AuthorizeURL changed from %q to %q", initial.AuthorizeURL, cfg.AuthorizeURL)
			}
		})
	}
}

func TestWithCallbackDomain_RejectsMissingCustomAndArbitraryHosts(t *testing.T) {
	cfg, err := ConfigForSite(DefaultStagingSite, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{
		"",
		"acme.datadoghq.com",
		"api.datadoghq.com",
		"https://datadoghq.com",
		"datadoghq.com.evil.example",
		"evil.example",
	} {
		t.Run(domain, func(t *testing.T) {
			if _, err := cfg.WithCallbackDomain(domain); err == nil {
				t.Fatalf("WithCallbackDomain(%q) succeeded", domain)
			}
		})
	}
}

func TestNormalizeSiteRejectsUnsafeInput(t *testing.T) {
	for _, raw := range []string{
		"http://dd.datad0g.com",
		"https://user@example.com",
		"https://dd.datad0g.com/path",
		"https://dd.datad0g.com?x=1",
		"https://evil.us3.example.com",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, _, err := normalizeSite(raw); err == nil {
				t.Fatalf("normalizeSite(%q) succeeded", raw)
			}
		})
	}
}

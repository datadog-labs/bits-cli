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
	if cfg.RedirectURI != "http://127.0.0.1:0/oauth/callback" {
		t.Errorf("RedirectURI = %q", cfg.RedirectURI)
	}

	cfg, err = cfg.WithCallbackDomain("datad0g.com")
	if err != nil {
		t.Fatalf("WithCallbackDomain: %v", err)
	}
	if cfg.TokenURL != "https://api.datad0g.com/oauth2/v1/token" {
		t.Errorf("TokenURL = %q", cfg.TokenURL)
	}
	if cfg.AssistantBase != "https://api.datad0g.com" {
		t.Errorf("AssistantBase = %q", cfg.AssistantBase)
	}
}

func TestConfigForSite_SelectsProductionClient(t *testing.T) {
	for _, test := range []struct {
		name     string
		site     string
		wantSite string
	}{
		{name: "default", wantSite: DefaultSite},
		{name: "US1", site: "https://app.datadoghq.com", wantSite: "https://app.datadoghq.com"},
		{name: "regional customer subdomain", site: "https://acme.us3.datadoghq.com", wantSite: "https://acme.us3.datadoghq.com"},
		{name: "EU", site: "https://app.datadoghq.eu", wantSite: "https://app.datadoghq.eu"},
		{name: "preprod", site: "https://ddstaging.datadoghq.com", wantSite: "https://ddstaging.datadoghq.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := ConfigForSite(test.site, "")
			if err != nil {
				t.Fatalf("ConfigForSite: %v", err)
			}
			if cfg.Site != test.wantSite {
				t.Errorf("Site = %q, want %q", cfg.Site, test.wantSite)
			}
			if cfg.ClientID != ProductionClientID {
				t.Errorf("ClientID = %q, want %q", cfg.ClientID, ProductionClientID)
			}
		})
	}
}

func TestConfigForSite_ClientOverrideTakesPrecedence(t *testing.T) {
	for _, site := range []string{DefaultStagingSite, DefaultSite} {
		cfg, err := ConfigForSite(site, "override-id")
		if err != nil {
			t.Fatalf("ConfigForSite(%q): %v", site, err)
		}
		if cfg.ClientID != "override-id" {
			t.Errorf("ConfigForSite(%q) ClientID = %q", site, cfg.ClientID)
		}
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

func TestConfigForSite_PreservesCustomerLoginDomain(t *testing.T) {
	cfg, err := ConfigForSite("https://acme.us3.datadoghq.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthorizeURL != "https://acme.us3.datadoghq.com/oauth2/v1/authorize" {
		t.Errorf("AuthorizeURL = %q", cfg.AuthorizeURL)
	}

	cfg, err = cfg.WithCallbackDomain("us3.datadoghq.com")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AssistantBase != "https://api.us3.datadoghq.com" {
		t.Errorf("AssistantBase = %q", cfg.AssistantBase)
	}
}

func TestConfigForSite_AcceptsGovDomainWithExplicitClient(t *testing.T) {
	_, err := ConfigForSite("https://customer.ddog-gov.com", "")
	if err == nil || !strings.Contains(err.Error(), "BITS_OAUTH_CLIENT_ID") {
		t.Fatalf("error = %v, want missing GovCloud client", err)
	}

	cfg, err := ConfigForSite("https://customer.ddog-gov.com", "gov-client-id")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthorizeURL != "https://customer.ddog-gov.com/oauth2/v1/authorize" {
		t.Errorf("AuthorizeURL = %q", cfg.AuthorizeURL)
	}

	cfg, err = cfg.WithCallbackDomain("ddog-gov.com")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AssistantBase != "https://api.ddog-gov.com" {
		t.Errorf("AssistantBase = %q", cfg.AssistantBase)
	}
}

func TestWithCallbackDomain_UnknownRegionWorksByDefault(t *testing.T) {
	initial, err := ConfigForSite("https://customer.xy9.datadoghq.com", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := initial.WithCallbackDomain("  XY9.DATADOGHQ.COM  ")
	if err != nil {
		t.Fatal(err)
	}
	apiBase := "https://api.xy9.datadoghq.com"
	if cfg.Site != apiBase || cfg.AssistantBase != apiBase {
		t.Errorf("routing = Site %q, AssistantBase %q; want %q", cfg.Site, cfg.AssistantBase, apiBase)
	}
	if cfg.TokenURL != apiBase+"/oauth2/v1/token" || cfg.RevokeURL != apiBase+"/oauth2/v1/revoke" {
		t.Errorf("OAuth endpoints = token %q, revoke %q", cfg.TokenURL, cfg.RevokeURL)
	}
	if cfg.AuthorizeURL != initial.AuthorizeURL {
		t.Errorf("AuthorizeURL changed from %q to %q", initial.AuthorizeURL, cfg.AuthorizeURL)
	}
}

func TestWithCallbackDomain_AllowsCommercialFamilyChange(t *testing.T) {
	cfg, err := ConfigForSite("https://app.datadoghq.com", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = cfg.WithCallbackDomain("datadoghq.eu")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AssistantBase != "https://api.datadoghq.eu" {
		t.Fatalf("AssistantBase = %q", cfg.AssistantBase)
	}
}

func TestWithCallbackDomain_RejectsDifferentEnvironment(t *testing.T) {
	for _, test := range []struct {
		name     string
		site     string
		clientID string
		callback string
	}{
		{name: "staging to commercial", site: DefaultStagingSite, callback: "datadoghq.com"},
		{name: "commercial to staging", site: DefaultSite, callback: "datad0g.com"},
		{name: "commercial to GovCloud", site: DefaultSite, callback: "ddog-gov.com"},
		{name: "GovCloud to commercial", site: "https://app.ddog-gov.com", clientID: "gov-client", callback: "datadoghq.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := ConfigForSite(test.site, test.clientID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.WithCallbackDomain(test.callback); err == nil {
				t.Fatalf("WithCallbackDomain(%q) succeeded", test.callback)
			}
		})
	}
}

func TestWithCallbackDomain_RejectsMissingAndNonDatadogHosts(t *testing.T) {
	cfg, err := ConfigForSite(DefaultStagingSite, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{
		"",
		"api.datadoghq.com",
		"https://datadoghq.com",
		"datadoghq.com:443",
		"datadoghq.com/path",
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
		"https://dd.datad0g.com:8443",
		"https://dd.datad0g.com/path",
		"https://dd.datad0g.com?x=1",
		"https://evil.us3.example.com",
		"https://datadoghq.com.evil.example",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, _, err := normalizeSite(raw); err == nil {
				t.Fatalf("normalizeSite(%q) succeeded", raw)
			}
		})
	}
}

func TestNormalizeSiteLowercasesHost(t *testing.T) {
	site, domain, err := normalizeSite("https://DD.DataD0g.com")
	if err != nil {
		t.Fatalf("normalizeSite: %v", err)
	}
	if site != "https://dd.datad0g.com" {
		t.Errorf("site = %q", site)
	}
	if domain != "dd.datad0g.com" {
		t.Errorf("domain = %q", domain)
	}
}

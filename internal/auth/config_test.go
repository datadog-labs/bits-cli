package auth

import (
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func wantStr(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func TestConfigForSite_StagingEnvironment(t *testing.T) {
	cfg, err := ConfigForSite("dd.datad0g.com", "staging-client")
	if err != nil {
		t.Fatalf("ConfigForSite: %v", err)
	}
	wantStr(t, "Site", cfg.Site, "https://dd.datad0g.com")
	wantStr(t, "ClientID", cfg.ClientID, "staging-client")
	wantStr(t, "AuthorizeURL", cfg.AuthorizeURL, "https://dd.datad0g.com/oauth2/v1/authorize")
	wantStr(t, "TokenURL", cfg.TokenURL, "https://api.dd.datad0g.com/api/v2/oauth2/token")
	wantStr(t, "AssistantBase", cfg.AssistantBase, "https://api.dd.datad0g.com")
	wantStr(t, "RedirectURI", cfg.RedirectURI, "http://127.0.0.1:0/oauth/callback")

	cfg, err = cfg.WithCallbackDomain("datad0g.com")
	if err != nil {
		t.Fatalf("WithCallbackDomain: %v", err)
	}
	wantStr(t, "callback Site", cfg.Site, "https://api.datad0g.com")
	wantStr(t, "callback TokenURL", cfg.TokenURL, "https://api.datad0g.com/api/v2/oauth2/token")
	wantStr(t, "callback RevokeURL", cfg.RevokeURL, "https://api.datad0g.com/oauth2/v1/revoke")
	wantStr(t, "callback AssistantBase", cfg.AssistantBase, "https://api.datad0g.com")

	// Staging has no built-in client: the ID is never compiled in.
	for _, rawSite := range []string{"dd.datad0g.com", "api.datad0g.com"} {
		if _, err := ConfigForSite(rawSite, ""); err == nil || !strings.Contains(err.Error(), "OAuth client ID override") {
			t.Errorf("ConfigForSite(%q) error = %v; want a missing-client error", rawSite, err)
		}
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
		{name: "customer subdomain", site: "https://demo.datadoghq.com", wantSite: "https://demo.datadoghq.com"},
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

func TestConfigForSite_TrimsClientOverride(t *testing.T) {
	cfg, err := ConfigForSite(DefaultSite, "  override-id\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "override-id" {
		t.Fatalf("ClientID = %q, want trimmed override", cfg.ClientID)
	}
}

func TestAuthorizationURL_UsesPKCEWithoutExplicitScope(t *testing.T) {
	cfg, err := ConfigForSite(DefaultSite, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := cfg.OAuth2Config().AuthCodeURL("state", oauth2.S256ChallengeOption("challenge"))
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	if query.Get("client_id") != ProductionClientID || query.Get("state") != "state" {
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

func TestNormalizeAPISite(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "api.datadoghq.com", want: "https://api.datadoghq.com"},
		{raw: "https://API.US3.DATADOGHQ.COM/", want: "https://api.us3.datadoghq.com"},
		{raw: "api.datad0g.com", want: "https://api.datad0g.com"},
		{raw: "dd.datad0g.com", want: "https://dd.datad0g.com"},
	} {
		got, err := NormalizeAPISite(test.raw)
		if err != nil || got != test.want {
			t.Errorf("NormalizeAPISite(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}

	for _, raw := range []string{
		"",
		"app.datadoghq.com",
		"app.datad0g.com",
		"https://example.com",
		"http://api.datadoghq.com",
		"https://api.datadoghq.com/path",
	} {
		if _, err := NormalizeAPISite(raw); err == nil {
			t.Errorf("NormalizeAPISite(%q) succeeded", raw)
		}
	}
}

func TestConfigForSite_AcceptsGovDomainWithExplicitClient(t *testing.T) {
	_, err := ConfigForSite("https://customer.ddog-gov.com", "")
	if err == nil || !strings.Contains(err.Error(), "OAuth client ID override") {
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
	if cfg.TokenURL != apiBase+"/api/v2/oauth2/token" || cfg.RevokeURL != apiBase+"/oauth2/v1/revoke" {
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
		{name: "commercial to GovCloud", site: DefaultSite, callback: "ddog-gov.com"},
		{name: "commercial to staging", site: DefaultSite, callback: "datad0g.com"},
		{name: "staging to commercial", site: "https://dd.datad0g.com", clientID: "staging-client", callback: "datadoghq.com"},
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
	cfg, err := ConfigForSite(DefaultSite, "")
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
		"http://app.datadoghq.com",
		"https://user@example.com",
		"https://app.datadoghq.com:8443",
		"https://app.datadoghq.com/path",
		"https://app.datadoghq.com?x=1",
		// Empty port and delimiter syntax: url.Parse drops these, so the raw
		// input needs the extra checks in normalizeSite.
		"https://app.datadoghq.com:",
		"https://app.datadoghq.com?",
		"https://app.datadoghq.com#",
		"https://app.datadoghq.com/%2f",
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
	site, domain, err := normalizeSite("https://APP.DataDogHQ.com")
	if err != nil {
		t.Fatalf("normalizeSite: %v", err)
	}
	if site != "https://app.datadoghq.com" {
		t.Errorf("site = %q", site)
	}
	if domain != "app.datadoghq.com" {
		t.Errorf("domain = %q", domain)
	}
}

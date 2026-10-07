package auth

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/datadog-labs/bits-cli/internal/site"
	"golang.org/x/oauth2"
)

// Staging is opt-in: unit tests pin the staging environment per test with
// t.Setenv so they stay deterministic under any configuration inherited from
// the developer's shell, while the opt-in BITS_OAUTH_E2E test keeps whatever
// real environment `bits login` used.

// noStagingEnv clears inherited staging configuration for one test. Empty
// values are equivalent to unset for the staging loader.
func noStagingEnv(t *testing.T) {
	t.Helper()
	t.Setenv(site.EnvStagingSite, "")
	t.Setenv(site.EnvStagingDomain, "")
	t.Setenv(site.EnvStagingClientID, "")
}

// stagingEnv configures one synthetic staging environment; the hosts and
// client ID are fictional .test values, never a real deployment.
func stagingEnv(t *testing.T) {
	t.Helper()
	t.Setenv(site.EnvStagingSite, "https://login.staging.test")
	t.Setenv(site.EnvStagingDomain, "staging.test")
	t.Setenv(site.EnvStagingClientID, "staging-test-client")
}

func wantStr(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func TestConfigForSite_StagingEnvironment(t *testing.T) {
	stagingEnv(t)
	cfg, err := ConfigForSite("login.staging.test", "")
	if err != nil {
		t.Fatalf("ConfigForSite: %v", err)
	}
	wantStr(t, "Site", cfg.Site, "https://login.staging.test")
	wantStr(t, "ClientID", cfg.ClientID, "staging-test-client")
	wantStr(t, "AuthorizeURL", cfg.AuthorizeURL, "https://login.staging.test/oauth2/v1/authorize")
	wantStr(t, "TokenURL", cfg.TokenURL, "https://api.staging.test/api/v2/oauth2/token")
	wantStr(t, "RevokeURL", cfg.RevokeURL, "https://api.staging.test/oauth2/v1/revoke")
	wantStr(t, "AssistantBase", cfg.AssistantBase, "https://api.staging.test")
	wantStr(t, "RedirectURI", cfg.RedirectURI, "http://127.0.0.1:0/oauth/callback")

	cfg, err = cfg.WithCallbackDomain("staging.test")
	if err != nil {
		t.Fatalf("WithCallbackDomain: %v", err)
	}
	wantStr(t, "callback Site", cfg.Site, "https://api.staging.test")
	wantStr(t, "callback TokenURL", cfg.TokenURL, "https://api.staging.test/api/v2/oauth2/token")
	wantStr(t, "callback AssistantBase", cfg.AssistantBase, "https://api.staging.test")

	// Staging configured still accepts production API hosts, and the configured
	// login origin canonicalizes to the environment's API host for API-key use.
	for _, test := range []struct{ raw, want string }{
		{"api.staging.test", "https://api.staging.test"},
		{"https://LOGIN.STAGING.TEST/", "https://api.staging.test"},
		{"api.datadoghq.com", "https://api.datadoghq.com"},
	} {
		got, err := NormalizeAPISite(test.raw)
		if err != nil || got != test.want {
			t.Errorf("NormalizeAPISite(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}

	cfg, err = ConfigForSite("https://login.staging.test/", "override-id")
	if err != nil {
		t.Fatal(err)
	}
	wantStr(t, "override ClientID", cfg.ClientID, "override-id")
	cfg, err = ConfigForSite("https://api.staging.test", "saved-client")
	if err != nil {
		t.Fatal(err)
	}
	wantStr(t, "saved ClientID", cfg.ClientID, "saved-client")
	wantStr(t, "saved TokenURL", cfg.TokenURL, "https://api.staging.test/api/v2/oauth2/token")

	t.Setenv(site.EnvStagingClientID, "")
	for _, rawSite := range []string{"login.staging.test", "api.staging.test"} {
		if _, err := ConfigForSite(rawSite, ""); err == nil || !strings.Contains(err.Error(), site.EnvStagingClientID) {
			t.Errorf("ConfigForSite(%q) error = %v; want a missing-client error naming %s", rawSite, err, site.EnvStagingClientID)
		}
	}
}

func TestConfigForSite_SelectsProductionClient(t *testing.T) {
	noStagingEnv(t)
	for _, test := range []struct {
		name     string
		staging  bool
		site     string
		wantSite string
	}{
		{name: "default", wantSite: DefaultSite},
		{name: "default with staging environment", staging: true, wantSite: DefaultSite},
		{name: "US1", site: "https://app.datadoghq.com", wantSite: "https://app.datadoghq.com"},
		{name: "US1 with staging environment", staging: true, site: "https://app.datadoghq.com", wantSite: "https://app.datadoghq.com"},
		{name: "regional customer subdomain", site: "https://acme.us3.datadoghq.com", wantSite: "https://acme.us3.datadoghq.com"},
		{name: "EU", site: "https://app.datadoghq.eu", wantSite: "https://app.datadoghq.eu"},
		{name: "EU with staging environment", staging: true, site: "https://app.datadoghq.eu", wantSite: "https://app.datadoghq.eu"},
		{name: "customer subdomain", site: "https://demo.datadoghq.com", wantSite: "https://demo.datadoghq.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.staging {
				stagingEnv(t)
			}
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

	// A configured staging default never satisfies GovCloud's requirement for
	// an explicit client.
	stagingEnv(t)
	if _, err := ConfigForSite("https://customer.ddog-gov.com", ""); err == nil {
		t.Fatal("GovCloud site accepted the configured staging client")
	}
}

func TestConfigForSite_TrimsClientOverride(t *testing.T) {
	noStagingEnv(t)
	cfg, err := ConfigForSite(DefaultSite, "  override-id\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ClientID != "override-id" {
		t.Fatalf("ClientID = %q, want trimmed override", cfg.ClientID)
	}
}

func TestConfigForSite_ClientOverrideTakesPrecedence(t *testing.T) {
	stagingEnv(t)
	for _, rawSite := range []string{"login.staging.test", DefaultSite} {
		cfg, err := ConfigForSite(rawSite, "override-id")
		if err != nil {
			t.Fatalf("ConfigForSite(%q): %v", rawSite, err)
		}
		if cfg.ClientID != "override-id" {
			t.Errorf("ConfigForSite(%q) ClientID = %q", rawSite, cfg.ClientID)
		}
	}
}

func TestAuthorizationURL_UsesPKCEWithoutExplicitScope(t *testing.T) {
	noStagingEnv(t)
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
	noStagingEnv(t)
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

// TestStagingRejectsSpoofAndUnconfiguredHosts runs one adversarial host list
// through every entry point that accepts a staging host: site selection, API
// site normalization, and the OAuth callback domain. Siblings and deeper
// children of the configured domain, its suffix spoofs, and unconfigured
// lookalikes of the production families must all be rejected.
func TestStagingRejectsSpoofAndUnconfiguredHosts(t *testing.T) {
	stagingEnv(t)
	cfg, err := ConfigForSite("login.staging.test", "staging-test-client")
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{
		"evil.staging.test",
		"api.evil.staging.test",
		"x.login.staging.test",
		"login.staging.test.evil.example",
		"staging.test.evil.example",
		"dd.datadoghq.example.test",
		"api.datadoghq.example.test",
		"acme.us3.datadoghq.com.evil.example",
	} {
		t.Run(host, func(t *testing.T) {
			if _, err := ConfigForSite(host, "staging-test-client"); err == nil {
				t.Errorf("ConfigForSite(%q) succeeded", host)
			}
			if _, err := NormalizeAPISite(host); err == nil {
				t.Errorf("NormalizeAPISite(%q) succeeded", host)
			}
			if _, err := cfg.WithCallbackDomain(host); err == nil {
				t.Errorf("WithCallbackDomain(%q) succeeded", host)
			}
		})
	}

	// The bare callback domain is not a login or API site input, though it is
	// the only domain the callback may return.
	if _, err := ConfigForSite("staging.test", ""); err == nil {
		t.Error("ConfigForSite accepted the bare callback domain as a site")
	}
	if _, err := NormalizeAPISite("staging.test"); err == nil {
		t.Error("NormalizeAPISite accepted the bare callback domain")
	}
}

// Staging hosts are rejected without configuration, and partial configuration
// fails loudly rather than being silently ignored.
func TestStagingEnvironmentMustBeFullyConfigured(t *testing.T) {
	noStagingEnv(t)
	for _, rawSite := range []string{"login.staging.test", "api.staging.test", "staging.test"} {
		if _, err := ConfigForSite(rawSite, ""); err == nil {
			t.Fatalf("ConfigForSite(%q) succeeded without staging configuration", rawSite)
		}
	}

	t.Setenv(site.EnvStagingSite, "https://login.staging.test")
	for _, name := range []string{"https://app.datadoghq.com", "login.staging.test"} {
		if _, err := ConfigForSite(name, ""); err == nil {
			t.Errorf("ConfigForSite(%q) succeeded with partial staging configuration", name)
		}
		if _, err := NormalizeAPISite(name); err == nil {
			t.Errorf("NormalizeAPISite(%q) succeeded with partial staging configuration", name)
		}
	}
}

func TestConfigForSite_StagingEnvChangeAfterConfigDoesNotRetargetCallback(t *testing.T) {
	stagingEnv(t)
	cfg, err := ConfigForSite("login.staging.test", "")
	if err != nil {
		t.Fatal(err)
	}

	// The environment changes mid-flow; the captured trust snapshot must still
	// decide which callback domain the in-flight flow accepts.
	t.Setenv(site.EnvStagingSite, "https://login.other.test")
	t.Setenv(site.EnvStagingDomain, "other.test")
	t.Setenv(site.EnvStagingClientID, "other-client")

	if _, err := cfg.WithCallbackDomain("other.test"); err == nil {
		t.Fatal("WithCallbackDomain accepted the retargeted environment's domain")
	}
	retargeted, err := cfg.WithCallbackDomain("staging.test")
	if err != nil {
		t.Fatalf("WithCallbackDomain with the captured domain: %v", err)
	}
	if retargeted.AssistantBase != "https://api.staging.test" {
		t.Errorf("AssistantBase = %q, want the captured environment's API host", retargeted.AssistantBase)
	}
}

// The staging callback accepts only its own canonical domain: nothing else,
// and a production flow never accepts a staging domain.
func TestWithCallbackDomain_StagingRejectsOtherDomains(t *testing.T) {
	stagingEnv(t)
	for _, test := range []struct{ site, callback string }{
		{"login.staging.test", ""},
		{"login.staging.test", "api.staging.test"},
		{"login.staging.test", "login.staging.test"},
		{"login.staging.test", "datadoghq.com"},
		{"login.staging.test", "datadoghq.eu"},
		{"login.staging.test", "ddog-gov.com"},
		{DefaultSite, "staging.test"},
	} {
		t.Run(test.callback, func(t *testing.T) {
			cfg, err := ConfigForSite(test.site, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.WithCallbackDomain(test.callback); err == nil {
				t.Fatalf("WithCallbackDomain(%q) succeeded", test.callback)
			}
		})
	}
}

func TestNormalizeAPISite(t *testing.T) {
	noStagingEnv(t)
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "api.datadoghq.com", want: "https://api.datadoghq.com"},
		{raw: "https://API.US3.DATADOGHQ.COM/", want: "https://api.us3.datadoghq.com"},
	} {
		got, err := NormalizeAPISite(test.raw)
		if err != nil || got != test.want {
			t.Errorf("NormalizeAPISite(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}

	for _, raw := range []string{
		"",
		"app.datadoghq.com",
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
	noStagingEnv(t)
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
	noStagingEnv(t)
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
	noStagingEnv(t)
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
	noStagingEnv(t)
	for _, test := range []struct {
		name     string
		site     string
		clientID string
		callback string
	}{
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
	noStagingEnv(t)
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
			if _, _, err := normalizeSite(raw, nil); err == nil {
				t.Fatalf("normalizeSite(%q) succeeded", raw)
			}
		})
	}
}

func TestNormalizeSiteLowercasesHost(t *testing.T) {
	site, domain, err := normalizeSite("https://APP.DataDogHQ.com", nil)
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

// A saved staging session is identified by the canonical API domain of the
// configured callback domain plus its stored client ID, never by the login UI
// alias: a different login alias within the same canonical domain keeps the
// stored tokens usable and keeps credential traffic on api.<domain>, while
// changing the callback domain itself invalidates the stored session and
// requires an explicit new login. No persisted staging-origin field exists,
// so the in-flight callback snapshot stays immutable.
func TestStagingSessionIdentityIsCanonicalAPIDomain(t *testing.T) {
	stagingEnv(t)
	cfg, err := ConfigForSite("login.staging.test", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = cfg.WithCallbackDomain("staging.test")
	if err != nil {
		t.Fatalf("WithCallbackDomain: %v", err)
	}
	saved := Session{
		Site:        cfg.Site,
		ClientID:    cfg.ClientID,
		AccessToken: "access",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(time.Hour),
	}
	if saved.Site != "https://api.staging.test" {
		t.Fatalf("saved session site = %q, want the canonical API domain", saved.Site)
	}

	// The login alias changes within the same canonical domain: the stored
	// session stays usable and its refresh and Assistant routes are rebuilt
	// from the saved site, not from the alias.
	t.Setenv(site.EnvStagingSite, "https://ui.staging.test")
	source, err := NewSource(saved, newMemoryStore(saved), nil)
	if err != nil {
		t.Fatalf("NewSource after login alias change: %v", err)
	}
	if got := source.Site(); got != "https://api.staging.test" {
		t.Errorf("Assistant base = %q, want the canonical API host", got)
	}
	if source.config.TokenURL != "https://api.staging.test/api/v2/oauth2/token" {
		t.Errorf("token URL = %q, want the canonical API host", source.config.TokenURL)
	}
	if source.config.ClientID != cfg.ClientID {
		t.Errorf("client ID = %q, want the stored client to stay authoritative", source.config.ClientID)
	}

	// The callback domain itself changes: the stored session can no longer be
	// used, because its api-prefixed site belongs to a different environment.
	t.Setenv(site.EnvStagingSite, "https://login.other.test")
	t.Setenv(site.EnvStagingDomain, "other.test")
	t.Setenv(site.EnvStagingClientID, "other-client")
	if _, err := NewSource(saved, newMemoryStore(saved), nil); err == nil {
		t.Fatal("NewSource accepted a stored session from a different staging domain")
	} else if !errors.Is(err, ErrSiteConfiguration) {
		t.Fatalf("NewSource error = %v, want ErrSiteConfiguration", err)
	}
}

// A valid staging login alias can itself start with "api.": it must still
// canonicalize to the environment's API host before the generic api-prefix
// admission, so API-key traffic never goes to the login origin. Ordinary
// staging login and API hosts and production hosts keep their behavior.
func TestNormalizeAPISiteCanonicalizesStagingLoginHostBeforeAPIPrefix(t *testing.T) {
	t.Setenv(site.EnvStagingSite, "https://api.login.staging.test")
	t.Setenv(site.EnvStagingDomain, "staging.test")
	t.Setenv(site.EnvStagingClientID, "staging-test-client")
	for _, raw := range []string{"api.login.staging.test", "https://API.LOGIN.STAGING.TEST/"} {
		got, err := NormalizeAPISite(raw)
		if err != nil || got != "https://api.staging.test" {
			t.Errorf("NormalizeAPISite(%q) = %q, %v; want the canonical API host %q", raw, got, err, "https://api.staging.test")
		}
	}
	for _, test := range []struct{ raw, want string }{
		{"api.staging.test", "https://api.staging.test"},
		{"api.datadoghq.com", "https://api.datadoghq.com"},
	} {
		got, err := NormalizeAPISite(test.raw)
		if err != nil || got != test.want {
			t.Errorf("NormalizeAPISite(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}
	if _, err := NormalizeAPISite("app.datadoghq.com"); err == nil {
		t.Error("NormalizeAPISite accepted a production login host")
	}
	if _, err := NormalizeAPISite("api.other.staging.test"); err == nil {
		t.Error("NormalizeAPISite accepted an unconfigured api-prefixed staging host")
	}
}

// Every built-in domain family must be protected from staging overlap in the
// staging loader; if the family list and the loader's protected suffixes drift,
// this fails instead of letting a new family be silently shadowed.
func TestStagingConfigCannotOverlapBuiltInDomainFamilies(t *testing.T) {
	for _, family := range datadogDomainFamilies {
		for _, test := range []struct{ name, rawSite, domain string }{
			{name: "domain equals family", rawSite: "https://login." + family.suffix, domain: family.suffix},
			{name: "domain under family", rawSite: "https://login.staging." + family.suffix, domain: "staging." + family.suffix},
			{name: "login host equals family", rawSite: "https://" + family.suffix, domain: "staging.test"},
		} {
			t.Run(family.suffix+" "+test.name, func(t *testing.T) {
				t.Setenv(site.EnvStagingSite, test.rawSite)
				t.Setenv(site.EnvStagingDomain, test.domain)
				t.Setenv(site.EnvStagingClientID, "staging-test-client")
				_, ok, err := site.StagingFromEnv()
				if err == nil || ok {
					t.Fatalf("StagingFromEnv() = ok %v, err %v; want rejection", ok, err)
				}
				if !strings.Contains(err.Error(), "overlaps the built-in production domain") {
					t.Fatalf("error = %v, want the protected-overlap rejection", err)
				}
			})
		}
	}
}

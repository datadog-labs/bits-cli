package auth

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/site"
	"golang.org/x/oauth2"
)

const (
	// ProductionClientID is the dedicated Bits CLI native OAuth client replicated
	// from US1 to the commercial Datadog production regions.
	ProductionClientID = "83d1af23-fbe1-4f44-a604-405b2c3fbdd9"
	// DefaultSite is the production US1 login site. The authorization flow lets a
	// user select another region and returns its canonical domain on the callback.
	DefaultSite = "https://app.datadoghq.com"
	// DefaultRedirectURI asks the OS to select an available IPv4 loopback port.
	// Datadog's OAuth provider permits flexible ports for native loopback clients
	// when the literal host and registered path match RFC 8252.
	DefaultRedirectURI = "http://127.0.0.1:0/oauth/callback"
)

type oauthEnvironment uint8

const (
	oauthEnvironmentCommercial oauthEnvironment = iota + 1
	oauthEnvironmentGovCloud
)

// domainFamily is both the hostname trust boundary and the default OAuth client
// mapping. It lists domain families, not regions, so customer subdomains and new
// commercial regions work without a client release. An empty client ID means the
// family is recognized for explicit configurations but has no default registration.
type domainFamily struct {
	suffix      string
	clientID    string
	environment oauthEnvironment
}

// Adding a family or client must be an explicit reviewed change (families
// documented at https://docs.datadoghq.com/getting_started/site/). The
// commercial client is not registered for GovCloud, and staging has no
// built-in family: it is reachable only through the environment
// configuration validated by internal/site.
var datadogDomainFamilies = []domainFamily{
	{suffix: "datadoghq.com", clientID: ProductionClientID, environment: oauthEnvironmentCommercial},
	{suffix: "datadoghq.eu", clientID: ProductionClientID, environment: oauthEnvironmentCommercial},
	{suffix: "ddog-gov.com", environment: oauthEnvironmentGovCloud},
}

// ErrSiteConfiguration marks a failure to build the site configuration a
// stored session requires, such as missing or mismatched BITS_STAGING_*
// variables. The stored session itself may be intact: restoring the matching
// environment, or logging in again with it, makes it usable again.
var ErrSiteConfiguration = errors.New("stored OAuth session does not match the current site configuration")

// SiteConfig contains the site-specific OAuth and Assistant endpoints.
// staging is the trust snapshot captured at build time for an
// environment-configured staging site; nil means a built-in destination.
type SiteConfig struct {
	Site          string
	Domain        string
	ClientID      string
	AuthorizeURL  string
	TokenURL      string
	RevokeURL     string
	RedirectURI   string
	AssistantBase string

	staging *site.StagingConfig
}

// stagingSnapshot captures the environment-configured staging trust snapshot
// at an API boundary.
func stagingSnapshot() (*site.StagingConfig, error) {
	config, ok, err := site.StagingFromEnv()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &config, nil
}

func isStagingHost(domain string, staging *site.StagingConfig) bool {
	return staging != nil && (domain == staging.LoginHost || domain == staging.APIHost)
}

// NormalizeAPISite validates a Datadog-owned API endpoint and returns its
// canonical HTTPS URL, accepting a URL or hostname. A configured staging
// environment also accepts its exact login host, canonicalizing it to the
// environment's API host even when the login alias itself starts with
// "api."; API-key calls then follow the documented API route instead of the
// login origin.
func NormalizeAPISite(rawSite string) (string, error) {
	staging, err := stagingSnapshot()
	if err != nil {
		return "", err
	}
	siteURL, domain, err := normalizeSite(rawSite, staging)
	if err != nil {
		return "", err
	}
	if staging != nil && domain == staging.LoginHost {
		return "https://" + staging.APIHost, nil
	}
	if !strings.HasPrefix(domain, "api.") {
		return "", fmt.Errorf("datadog API site must use an api-prefixed hostname")
	}
	return siteURL, nil
}

// ConfigForSite preserves the supplied Datadog site as the authorization
// host, including customer subdomains. The site family selects the production
// or GovCloud registration unless clientIDOverride is provided. A configured
// staging environment accepts only its exact login and API hosts and never
// inherits a production client.
func ConfigForSite(rawSite, clientIDOverride string) (SiteConfig, error) {
	staging, err := stagingSnapshot()
	if err != nil {
		return SiteConfig{}, err
	}
	siteURL, domain, err := normalizeSite(rawSite, staging)
	if err != nil {
		return SiteConfig{}, err
	}

	clientID := strings.TrimSpace(clientIDOverride)
	var snapshot *site.StagingConfig
	if isStagingHost(domain, staging) {
		snapshot = staging
		if clientID == "" {
			clientID = staging.ClientID
		}
		if clientID == "" {
			return SiteConfig{}, fmt.Errorf("no OAuth client is configured for staging site %s; pass --client-id or set %s", domain, site.EnvStagingClientID)
		}
	} else if clientID == "" {
		clientID = defaultClientID(domain)
		if clientID == "" {
			return SiteConfig{}, fmt.Errorf("no Bits CLI OAuth client is configured for %s; provide an OAuth client ID override", domain)
		}
	}

	// Login replaces these provisional API routes with the canonical domain
	// returned by the OAuth service before exchanging the authorization code;
	// sessions already store that api-prefixed site, so this also reconstructs
	// their routes.
	apiDomain := domain
	if !strings.HasPrefix(apiDomain, "api.") {
		apiDomain = "api." + apiDomain
	}
	if snapshot != nil {
		apiDomain = snapshot.APIHost
	}
	apiBase := "https://" + apiDomain
	return SiteConfig{
		Site:          siteURL,
		Domain:        domain,
		ClientID:      clientID,
		AuthorizeURL:  siteURL + "/oauth2/v1/authorize",
		TokenURL:      apiBase + "/api/v2/oauth2/token",
		RevokeURL:     apiBase + "/oauth2/v1/revoke",
		RedirectURI:   DefaultRedirectURI,
		AssistantBase: apiBase,
		staging:       snapshot,
	}, nil
}

// WithCallbackDomain applies the canonical regional base returned by Datadog
// on the state-validated OAuth callback. Validation uses the trust snapshot
// captured when this configuration was built, so a mid-flow environment
// change cannot retarget the callback.
func (c SiteConfig) WithCallbackDomain(raw string) (SiteConfig, error) {
	domain, err := normalizeCallbackDomain(raw, c.staging)
	if err != nil {
		return SiteConfig{}, err
	}
	if c.staging == nil {
		initialDomain := c.Domain
		if initialDomain == "" {
			_, initialDomain, err = normalizeSite(c.Site, nil)
			if err != nil {
				return SiteConfig{}, fmt.Errorf("validate initial OAuth site: %w", err)
			}
		}
		if environmentForDomain(initialDomain) != environmentForDomain(domain) {
			return SiteConfig{}, fmt.Errorf("OAuth callback returned Datadog domain %q from a different environment", raw)
		}
	}

	apiBase := "https://api." + domain
	c.Site = apiBase
	c.Domain = domain
	c.TokenURL = apiBase + "/api/v2/oauth2/token"
	c.RevokeURL = apiBase + "/oauth2/v1/revoke"
	c.AssistantBase = apiBase
	return c, nil
}

// OAuth2Config returns a public-client configuration. Scopes are deliberately
// omitted: Datadog assigns the registered OIDC scopes and permissions, and an
// explicit scope caused invalid_scope responses for the legacy Bits client.
func (c SiteConfig) OAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:    c.ClientID,
		RedirectURL: c.RedirectURI,
		Endpoint: oauth2.Endpoint{
			AuthURL:   c.AuthorizeURL,
			TokenURL:  c.TokenURL,
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// normalizeCallbackDomain validates the bare domain returned on the OAuth
// callback: with a staging trust snapshot, only that environment's exact
// domain; otherwise the built-in production families.
func normalizeCallbackDomain(raw string, staging *site.StagingConfig) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	u, err := url.Parse("https://" + domain)
	if err != nil || domain == "" || u.Host != domain || u.Hostname() != domain || u.Port() != "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
	}
	if staging != nil {
		if domain != staging.Domain {
			return "", fmt.Errorf("OAuth callback returned domain %q from a different environment", raw)
		}
		return domain, nil
	}
	if !isDatadogDomain(domain) {
		return "", fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
	}
	if strings.HasPrefix(domain, "api.") {
		return "", fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
	}
	return domain, nil
}

func normalizeSite(raw string, staging *site.StagingConfig) (siteURL, domain string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultSite
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse Datadog site: %w", err)
	}
	if u.Scheme != "https" || u.Hostname() == "" {
		return "", "", fmt.Errorf("datadog site must be an https URL or hostname")
	}
	// url.Parse drops an empty port ("host:"), empty "?" and "#" delimiters,
	// and percent-encoded path characters, so the raw input and escaped path
	// need direct checks.
	if u.User != nil || u.Port() != "" || strings.HasSuffix(u.Host, ":") || strings.ContainsAny(raw, "?#") ||
		(u.Path != "" && u.Path != "/") || u.EscapedPath() != u.Path {
		return "", "", fmt.Errorf("datadog site must not include credentials, a port, a path, query, or fragment")
	}
	domain = strings.ToLower(u.Hostname())
	if !isDatadogDomain(domain) && !isStagingHost(domain, staging) {
		return "", "", fmt.Errorf("datadog site must use a Datadog-owned hostname; a staging host is reachable only by configuring %s and %s", site.EnvStagingSite, site.EnvStagingDomain)
	}
	return "https://" + domain, domain, nil
}

func isDatadogDomain(domain string) bool {
	for _, family := range datadogDomainFamilies {
		if hasDomainSuffix(domain, family.suffix) {
			return true
		}
	}
	return false
}

func defaultClientID(domain string) string {
	for _, family := range datadogDomainFamilies {
		if hasDomainSuffix(domain, family.suffix) {
			return family.clientID
		}
	}
	return ""
}

func environmentForDomain(domain string) oauthEnvironment {
	for _, family := range datadogDomainFamilies {
		if hasDomainSuffix(domain, family.suffix) {
			return family.environment
		}
	}
	return 0
}

func hasDomainSuffix(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

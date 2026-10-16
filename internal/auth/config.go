package auth

import (
	"fmt"
	"net/url"
	"strings"

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
	oauthEnvironmentStaging oauthEnvironment = iota + 1
	oauthEnvironmentCommercial
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
// commercial client is not registered for GovCloud, and the staging client ID
// is deliberately not built in: a staging login names it explicitly.
var datadogDomainFamilies = []domainFamily{
	{suffix: "datad0g.com", environment: oauthEnvironmentStaging},
	{suffix: "datadoghq.com", clientID: ProductionClientID, environment: oauthEnvironmentCommercial},
	{suffix: "datadoghq.eu", clientID: ProductionClientID, environment: oauthEnvironmentCommercial},
	{suffix: "ddog-gov.com", environment: oauthEnvironmentGovCloud},
}

// SiteConfig contains the site-specific OAuth and Assistant endpoints.
type SiteConfig struct {
	Site          string
	Domain        string
	ClientID      string
	AuthorizeURL  string
	TokenURL      string
	RevokeURL     string
	RedirectURI   string
	AssistantBase string
}

// NormalizeAPISite validates a Datadog-owned API endpoint and returns its
// canonical HTTPS URL. It accepts either a URL or hostname. The staging host
// dd.datad0g.com is the sole non-api-prefixed endpoint supported by the
// Assistant API.
func NormalizeAPISite(rawSite string) (string, error) {
	site, domain, err := normalizeSite(rawSite)
	if err != nil {
		return "", err
	}
	if domain != "dd.datad0g.com" && !strings.HasPrefix(domain, "api.") {
		return "", fmt.Errorf("datadog API site must use an api-prefixed hostname")
	}
	return site, nil
}

// ConfigForSite preserves the supplied Datadog site as the authorization host,
// including customer subdomains. The site family selects the commercial
// production registration unless clientIDOverride is provided; staging and
// GovCloud have no built-in client and require the override.
func ConfigForSite(rawSite, clientIDOverride string) (SiteConfig, error) {
	site, domain, err := normalizeSite(rawSite)
	if err != nil {
		return SiteConfig{}, err
	}

	clientID := strings.TrimSpace(clientIDOverride)
	if clientID == "" {
		clientID = defaultClientID(domain)
	}
	if clientID == "" {
		return SiteConfig{}, fmt.Errorf("no Bits CLI OAuth client is configured for %s; provide an OAuth client ID override", domain)
	}

	// Login replaces these provisional API routes with the canonical domain
	// returned by the OAuth service before exchanging the authorization code;
	// sessions already store that api-prefixed site, so this also reconstructs
	// their routes.
	apiDomain := domain
	if !strings.HasPrefix(apiDomain, "api.") {
		apiDomain = "api." + apiDomain
	}
	apiBase := "https://" + apiDomain
	return SiteConfig{
		Site:          site,
		Domain:        domain,
		ClientID:      clientID,
		AuthorizeURL:  site + "/oauth2/v1/authorize",
		TokenURL:      apiBase + "/api/v2/oauth2/token",
		RevokeURL:     apiBase + "/oauth2/v1/revoke",
		RedirectURI:   DefaultRedirectURI,
		AssistantBase: apiBase,
	}, nil
}

// WithCallbackDomain applies the canonical regional base returned by Datadog
// on the state-validated OAuth callback. The OAuth service removes customer
// subdomains from this value, so api.<domain> routes token and Assistant
// requests correctly.
func (c SiteConfig) WithCallbackDomain(raw string) (SiteConfig, error) {
	domain, err := normalizeCallbackDomain(raw)
	if err != nil {
		return SiteConfig{}, err
	}
	initialDomain := c.Domain
	if initialDomain == "" {
		_, initialDomain, err = normalizeSite(c.Site)
		if err != nil {
			return SiteConfig{}, fmt.Errorf("validate initial OAuth site: %w", err)
		}
	}
	if environmentForDomain(initialDomain) != environmentForDomain(domain) {
		return SiteConfig{}, fmt.Errorf("OAuth callback returned Datadog domain %q from a different environment", raw)
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

func normalizeCallbackDomain(raw string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	u, err := url.Parse("https://" + domain)
	if err != nil || domain == "" || u.Host != domain || u.Hostname() != domain || u.Port() != "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		!isDatadogDomain(domain) || strings.HasPrefix(domain, "api.") {
		return "", fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
	}
	return domain, nil
}

func normalizeSite(raw string) (site, domain string, err error) {
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
	if !isDatadogDomain(domain) {
		return "", "", fmt.Errorf("datadog site must use a Datadog-owned hostname")
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

package auth

import (
	"fmt"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
)

const (
	// StagingClientID is the dedicated Bits CLI native OAuth client in Datadog staging.
	StagingClientID = "605736a5-3085-47c7-ab49-22b90d36530f"
	// DefaultStagingSite is the org-2 staging site used while the OAuth flow is validated.
	DefaultStagingSite = "https://dd.datad0g.com"
	// DefaultRedirectURI asks the OS to select an available IPv4 loopback port.
	// Datadog's OAuth provider permits flexible ports for native loopback clients
	// when the literal host and registered path match RFC 8252.
	DefaultRedirectURI = "http://127.0.0.1:0/oauth/callback"
)

// datadogDomainSuffixes is the trust boundary for OAuth login and callback
// hosts. It lists domain families, not regions, so new regional subdomains work
// automatically. Production families are documented at
// https://docs.datadoghq.com/getting_started/site/; datad0g.com is the internal
// staging family. Adding a new family must be an explicit reviewed change.
var datadogDomainSuffixes = []string{
	"datad0g.com",
	"datadoghq.com",
	"datadoghq.eu",
	"ddog-gov.com",
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

// ConfigForSite preserves the supplied Datadog site as the authorization host,
// including customer subdomains. clientIDOverride is required outside
// datad0g.com until the dedicated production client exists.
func ConfigForSite(rawSite, clientIDOverride string) (SiteConfig, error) {
	site, domain, err := normalizeSite(rawSite)
	if err != nil {
		return SiteConfig{}, err
	}

	clientID := clientIDOverride
	if clientID == "" && hasDomainSuffix(domain, "datad0g.com") {
		clientID = StagingClientID
	}
	if clientID == "" {
		return SiteConfig{}, fmt.Errorf("no Bits CLI OAuth client is configured for %s; set BITS_OAUTH_CLIENT_ID", domain)
	}

	// Login replaces these provisional API routes with the canonical domain
	// returned by AAA before exchanging the authorization code. Sessions already
	// store that api-prefixed site, so this also reconstructs their routes.
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
		TokenURL:      apiBase + "/oauth2/v1/token",
		RevokeURL:     apiBase + "/oauth2/v1/revoke",
		RedirectURI:   DefaultRedirectURI,
		AssistantBase: apiBase,
	}, nil
}

// WithCallbackDomain applies the canonical regional base returned by Datadog
// on the state-validated OAuth callback. AAA removes customer subdomains from
// this value, so api.<domain> routes token and Assistant requests correctly.
func (c SiteConfig) WithCallbackDomain(raw string) (SiteConfig, error) {
	domain, err := normalizeCallbackDomain(raw)
	if err != nil {
		return SiteConfig{}, err
	}

	apiBase := "https://api." + domain
	c.Site = apiBase
	c.Domain = domain
	c.TokenURL = apiBase + "/oauth2/v1/token"
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
	if err != nil || domain == "" || u.Host != domain || u.Hostname() != domain || u.Port() != "" || !isDatadogDomain(domain) {
		return "", fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
	}
	if strings.HasPrefix(domain, "api.") {
		return "", fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
	}
	return domain, nil
}

func normalizeSite(raw string) (site, domain string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultStagingSite
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
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", "", fmt.Errorf("datadog site must not include credentials, a path, query, or fragment")
	}
	domain = strings.ToLower(u.Hostname())
	if !isDatadogDomain(domain) {
		return "", "", fmt.Errorf("datadog site must use a Datadog-owned hostname")
	}
	return "https://" + u.Host, domain, nil
}

func isDatadogDomain(domain string) bool {
	for _, suffix := range datadogDomainSuffixes {
		if hasDomainSuffix(domain, suffix) {
			return true
		}
	}
	return false
}

func hasDomainSuffix(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

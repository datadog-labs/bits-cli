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

type regionConfig struct {
	canonicalDomain string
	authorizeDomain string
	apiDomain       string
	defaultClientID string
	matchSubdomains bool
	callbackAllowed bool
}

// datadogRegions is the single registry for initial-site routing and callback
// validation. More-specific production domains must precede datadoghq.com so
// customer subdomains resolve to their regional endpoints before the US1
// fallback. ddstaging is an initial-site alias; OAuth returns datadoghq.com as
// its canonical callback domain.
var datadogRegions = []regionConfig{
	{
		canonicalDomain: "datad0g.com",
		authorizeDomain: "dd.datad0g.com",
		apiDomain:       "api.datad0g.com",
		defaultClientID: StagingClientID,
		matchSubdomains: true,
		callbackAllowed: true,
	},
	{
		canonicalDomain: "ddstaging.datadoghq.com",
		authorizeDomain: "ddstaging.datadoghq.com",
		apiDomain:       "api.datadoghq.com",
	},
	{
		canonicalDomain: "datadoghq.eu",
		authorizeDomain: "app.datadoghq.eu",
		apiDomain:       "api.datadoghq.eu",
		matchSubdomains: true,
		callbackAllowed: true,
	},
	{
		canonicalDomain: "us3.datadoghq.com",
		authorizeDomain: "us3.datadoghq.com",
		apiDomain:       "api.us3.datadoghq.com",
		matchSubdomains: true,
		callbackAllowed: true,
	},
	{
		canonicalDomain: "us5.datadoghq.com",
		authorizeDomain: "us5.datadoghq.com",
		apiDomain:       "api.us5.datadoghq.com",
		matchSubdomains: true,
		callbackAllowed: true,
	},
	{
		canonicalDomain: "ap1.datadoghq.com",
		authorizeDomain: "ap1.datadoghq.com",
		apiDomain:       "api.ap1.datadoghq.com",
		matchSubdomains: true,
		callbackAllowed: true,
	},
	{
		canonicalDomain: "ap2.datadoghq.com",
		authorizeDomain: "ap2.datadoghq.com",
		apiDomain:       "api.ap2.datadoghq.com",
		matchSubdomains: true,
		callbackAllowed: true,
	},
	{
		canonicalDomain: "datadoghq.com",
		authorizeDomain: "app.datadoghq.com",
		apiDomain:       "api.datadoghq.com",
		matchSubdomains: true,
		callbackAllowed: true,
	},
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

// ConfigForSite resolves Datadog's regional OAuth endpoints. clientIDOverride
// is required outside datad0g.com until the dedicated production client exists.
func ConfigForSite(rawSite, clientIDOverride string) (SiteConfig, error) {
	site, domain, err := normalizeSite(rawSite)
	if err != nil {
		return SiteConfig{}, err
	}

	region, ok := regionForSite(domain)
	if !ok {
		return SiteConfig{}, fmt.Errorf("datadog site %q does not map to a supported region", domain)
	}
	clientID := region.defaultClientID
	if clientIDOverride != "" {
		clientID = clientIDOverride
	}
	if clientID == "" {
		return SiteConfig{}, fmt.Errorf("no Bits CLI OAuth client is configured for %s; set BITS_OAUTH_CLIENT_ID", domain)
	}

	return SiteConfig{
		Site:          site,
		Domain:        domain,
		ClientID:      clientID,
		AuthorizeURL:  "https://" + region.authorizeDomain + "/oauth2/v1/authorize",
		TokenURL:      "https://" + region.apiDomain + "/oauth2/v1/token",
		RevokeURL:     "https://" + region.apiDomain + "/oauth2/v1/revoke",
		RedirectURI:   DefaultRedirectURI,
		AssistantBase: "https://" + region.apiDomain,
	}, nil
}

// WithCallbackDomain applies the canonical domain returned by Datadog on the
// state-validated OAuth callback. Datadog intentionally removes customer
// subdomains from this value so token and API requests target the correct
// regional API host. Accept only documented base domains rather than treating
// callback input as an arbitrary hostname.
func (c SiteConfig) WithCallbackDomain(raw string) (SiteConfig, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	region, ok := regionForCallback(domain)
	if !ok {
		return SiteConfig{}, fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
	}

	apiBase := "https://" + region.apiDomain
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

func regionForSite(domain string) (regionConfig, bool) {
	for _, region := range datadogRegions {
		if domain == region.canonicalDomain ||
			(region.matchSubdomains && hasDomainSuffix(domain, region.canonicalDomain)) {
			return region, true
		}
	}
	return regionConfig{}, false
}

func regionForCallback(domain string) (regionConfig, bool) {
	for _, region := range datadogRegions {
		if region.callbackAllowed && domain == region.canonicalDomain {
			return region, true
		}
	}
	return regionConfig{}, false
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
	if !hasDomainSuffix(domain, "datad0g.com") &&
		!hasDomainSuffix(domain, "datadoghq.com") &&
		!hasDomainSuffix(domain, "datadoghq.eu") {
		return "", "", fmt.Errorf("datadog site must use a datad0g.com, datadoghq.com, or datadoghq.eu hostname")
	}
	return "https://" + u.Host, domain, nil
}

func hasDomainSuffix(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

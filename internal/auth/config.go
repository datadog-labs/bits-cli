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
	// DefaultRedirectURI is the loopback callback currently registered on the staging client.
	DefaultRedirectURI = "http://localhost:5000/step2"
)

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

	var authDomain, apiDomain, clientID string
	switch {
	case hasDomainSuffix(domain, "datad0g.com"):
		authDomain = "dd.datad0g.com"
		apiDomain = "api.datad0g.com"
		clientID = StagingClientID
	case domain == "ddstaging.datadoghq.com":
		authDomain = domain
		apiDomain = "api.datadoghq.com"
	case hasDomainSuffix(domain, "datadoghq.eu"):
		authDomain = "app.datadoghq.eu"
		apiDomain = "api.datadoghq.eu"
	case hasDomainSuffix(domain, "us3.datadoghq.com"):
		authDomain = "us3.datadoghq.com"
		apiDomain = "api.us3.datadoghq.com"
	case hasDomainSuffix(domain, "us5.datadoghq.com"):
		authDomain = "us5.datadoghq.com"
		apiDomain = "api.us5.datadoghq.com"
	case hasDomainSuffix(domain, "ap1.datadoghq.com"):
		authDomain = "ap1.datadoghq.com"
		apiDomain = "api.ap1.datadoghq.com"
	case hasDomainSuffix(domain, "ap2.datadoghq.com"):
		authDomain = "ap2.datadoghq.com"
		apiDomain = "api.ap2.datadoghq.com"
	default:
		authDomain = "app.datadoghq.com"
		apiDomain = "api.datadoghq.com"
	}
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
		AuthorizeURL:  "https://" + authDomain + "/oauth2/v1/authorize",
		TokenURL:      "https://" + apiDomain + "/oauth2/v1/token",
		RevokeURL:     "https://" + apiDomain + "/oauth2/v1/revoke",
		RedirectURI:   DefaultRedirectURI,
		AssistantBase: "https://" + apiDomain,
	}, nil
}

// WithCallbackDomain applies the canonical domain returned by Datadog on the
// state-validated OAuth callback. Datadog intentionally removes customer
// subdomains from this value so token and API requests target the correct
// regional API host. Accept only documented base domains rather than treating
// callback input as an arbitrary hostname.
func (c SiteConfig) WithCallbackDomain(raw string) (SiteConfig, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	switch domain {
	case "datad0g.com",
		"datadoghq.com",
		"datadoghq.eu",
		"us3.datadoghq.com",
		"us5.datadoghq.com",
		"ap1.datadoghq.com",
		"ap2.datadoghq.com":
	default:
		return SiteConfig{}, fmt.Errorf("OAuth callback returned an unsupported Datadog domain %q", raw)
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
		return "", "", fmt.Errorf("Datadog site must be an https URL or hostname")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", "", fmt.Errorf("Datadog site must not include credentials, a path, query, or fragment")
	}
	domain = strings.ToLower(u.Hostname())
	if !hasDomainSuffix(domain, "datad0g.com") &&
		!hasDomainSuffix(domain, "datadoghq.com") &&
		!hasDomainSuffix(domain, "datadoghq.eu") {
		return "", "", fmt.Errorf("Datadog site must use a datad0g.com, datadoghq.com, or datadoghq.eu hostname")
	}
	return "https://" + u.Host, domain, nil
}

func hasDomainSuffix(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

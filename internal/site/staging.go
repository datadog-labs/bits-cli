package site

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// Staging is opt-in through these environment variables; none is set by
// default. SITE and DOMAIN must be configured together; CLIENT_ID is optional.
const (
	// EnvStagingSite is the HTTPS login origin.
	EnvStagingSite = "BITS_STAGING_SITE"
	// EnvStagingDomain is the bare canonical callback domain; the API is
	// served at api.<domain>, so it cannot be derived by truncating
	// labels from the login host.
	EnvStagingDomain = "BITS_STAGING_DOMAIN"
	// EnvStagingClientID optionally names the default OAuth client; an
	// explicit --client-id override always wins.
	EnvStagingClientID = "BITS_STAGING_CLIENT_ID"
)

// protectedSuffixes are the built-in production domain families. A configured
// staging environment must not overlap any of them in either direction.
var protectedSuffixes = []string{"datadoghq.com", "datadoghq.eu", "ddog-gov.com"}

// StagingConfig is an immutable trust snapshot of one configured staging
// environment: the exact hosts it may use and nothing else. It is captured once
// at an API boundary and never re-read mid-flow, so a later environment change
// cannot widen trust for an in-flight operation.
type StagingConfig struct {
	LoginHost string
	Domain    string
	APIHost   string
	ClientID  string
}

// StagingFromEnv loads the staging environment configuration. ok is false only
// when none of the three variables is set; any partial or malformed
// combination is an error, because staging trust is exact and never inferred.
func StagingFromEnv() (config StagingConfig, ok bool, err error) {
	return stagingFromValues(
		os.Getenv(EnvStagingSite),
		os.Getenv(EnvStagingDomain),
		os.Getenv(EnvStagingClientID),
	)
}

func stagingFromValues(rawSite, rawDomain, rawClientID string) (StagingConfig, bool, error) {
	siteValue := strings.TrimSpace(rawSite)
	domainValue := strings.TrimSpace(rawDomain)
	clientID := strings.TrimSpace(rawClientID)
	if siteValue == "" && domainValue == "" && clientID == "" {
		return StagingConfig{}, false, nil
	}
	if siteValue == "" || domainValue == "" {
		return StagingConfig{}, false, fmt.Errorf(
			"staging configuration requires both %s and %s; %s alone does not configure a staging site",
			EnvStagingSite, EnvStagingDomain, EnvStagingClientID)
	}

	loginHost, err := validateStagingSite(siteValue)
	if err != nil {
		return StagingConfig{}, false, err
	}
	domain, err := validateStagingDomain(domainValue)
	if err != nil {
		return StagingConfig{}, false, err
	}
	for _, host := range []string{domain, loginHost} {
		if suffix, protected := protectedOverlap(host); protected {
			return StagingConfig{}, false, fmt.Errorf(
				"staging configuration %q overlaps the built-in production domain %q",
				host, suffix)
		}
	}
	if loginHost == "api."+domain {
		return StagingConfig{}, false, fmt.Errorf(
			"%s must not be api.%s; that host is reserved for the Assistant and token endpoints",
			EnvStagingSite, domain)
	}
	if loginHost != domain && !strings.HasSuffix(loginHost, "."+domain) {
		return StagingConfig{}, false, fmt.Errorf(
			"%s host %q must be %s or a subdomain of it",
			EnvStagingSite, loginHost, domain)
	}
	return StagingConfig{
		LoginHost: loginHost,
		Domain:    domain,
		APIHost:   "api." + domain,
		ClientID:  clientID,
	}, true, nil
}

// validateStagingSite accepts a hostname-only HTTPS origin; a trailing slash
// is the only extra syntax allowed.
func validateStagingSite(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", fmt.Errorf("%s must be an HTTPS login origin such as https://login.staging.test", EnvStagingSite)
	}
	// url.Parse drops empty ports ("host:"), empty "?" and "#" delimiters, and
	// percent-encoded paths, so the raw input and escaped path need checks.
	if u.User != nil || u.Port() != "" || strings.HasSuffix(u.Host, ":") || strings.ContainsAny(trimmed, "?#") ||
		(u.Path != "" && u.Path != "/") || u.EscapedPath() != u.Path {
		return "", fmt.Errorf("%s must be a hostname-only origin without credentials, port, path, query, or fragment", EnvStagingSite)
	}
	host := strings.ToLower(u.Hostname())
	if err := validHostnameLabels(host); err != nil {
		return "", fmt.Errorf("%s: %w", EnvStagingSite, err)
	}
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("%s must be a hostname, not an IP literal", EnvStagingSite)
	}
	return host, nil
}

func validateStagingDomain(raw string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	if domain == "" {
		return "", fmt.Errorf("%s is empty", EnvStagingDomain)
	}
	u, err := url.Parse("https://" + domain)
	if err != nil || u.User != nil || u.Port() != "" || u.Host != domain || u.Hostname() != domain ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s must be a bare domain such as staging.test, without scheme, port, path, or credentials", EnvStagingDomain)
	}
	if err := validHostnameLabels(domain); err != nil {
		return "", fmt.Errorf("%s: %w", EnvStagingDomain, err)
	}
	if net.ParseIP(domain) != nil {
		return "", fmt.Errorf("%s must be a domain, not an IP literal", EnvStagingDomain)
	}
	if strings.Count(domain, ".") < 1 {
		return "", fmt.Errorf("%s must have at least two labels such as staging.test", EnvStagingDomain)
	}
	return domain, nil
}

// validHostnameLabels enforces the hostname syntax the staging trust boundary
// relies on: nonempty 1-63 byte labels of alphanumerics and inner hyphens
// only, within the standard 253-byte total limit.
func validHostnameLabels(host string) error {
	if host == "" {
		return errors.New("hostname is empty")
	}
	if len(host) > 253 {
		return fmt.Errorf("hostname %q exceeds 253 characters", host)
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 {
			return fmt.Errorf("hostname %q contains an empty label", host)
		}
		if len(label) > 63 {
			return fmt.Errorf("hostname %q contains a label longer than 63 characters", host)
		}
		for i := range len(label) {
			c := label[i]
			isAlnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
			if !isAlnum && (c != '-' || i == 0 || i == len(label)-1) {
				return fmt.Errorf("hostname %q contains an invalid label %q", host, label)
			}
		}
	}
	return nil
}

// protectedOverlap reports the built-in production suffix overlapping host in
// either direction.
func protectedOverlap(host string) (string, bool) {
	for _, suffix := range protectedSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) || strings.HasSuffix(suffix, "."+host) {
			return suffix, true
		}
	}
	return "", false
}

// WebHostFor maps one of the configured staging Assistant hosts to the
// configured login host. Siblings, deeper children, and other environments
// never match.
func (c StagingConfig) WebHostFor(host string) (string, bool) {
	if host == c.LoginHost || host == c.APIHost {
		return c.LoginHost, true
	}
	return "", false
}

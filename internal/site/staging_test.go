package site

import (
	"strings"
	"testing"
)

func TestStagingFromValuesAccepts(t *testing.T) {
	config, ok, err := stagingFromValues(
		"  https://Login.Staging.Test/  ",
		" Staging.Test ",
		"  staging-test-client  ",
	)
	if err != nil || !ok {
		t.Fatalf("stagingFromValues: %v, ok = %v", err, ok)
	}
	want := StagingConfig{
		LoginHost: "login.staging.test",
		Domain:    "staging.test",
		APIHost:   "api.staging.test",
		ClientID:  "staging-test-client",
	}
	if config != want {
		t.Errorf("config = %#v, want %#v", config, want)
	}

	// The login origin may be the domain itself; the client ID is optional.
	config, ok, err = stagingFromValues("https://staging.test", "staging.test", "")
	if err != nil || !ok || config.LoginHost != config.Domain || config.ClientID != "" {
		t.Fatalf("login host at domain: %#v, %v, %v", config, ok, err)
	}

	// No variables at all means staging is unconfigured, not an error.
	config, ok, err = stagingFromValues("", " ", "  ")
	if err != nil || ok || config != (StagingConfig{}) {
		t.Fatalf("no variables: %#v, %v, %v", config, ok, err)
	}
}

func TestStagingFromValuesRejectsPartialConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		site   string
		domain string
		client string
	}{
		{name: "site only", site: "https://login.staging.test"},
		{name: "domain only", domain: "staging.test"},
		{name: "client ID only", client: "staging-test-client"},
		{name: "client ID with site only", site: "https://login.staging.test", client: "staging-test-client"},
		{name: "client ID with domain only", domain: "staging.test", client: "staging-test-client"},
	} {
		_, ok, err := stagingFromValues(test.site, test.domain, test.client)
		if err == nil || ok {
			t.Errorf("%s: stagingFromValues(%q, %q) accepted", test.name, test.site, test.domain)
		}
		if err != nil && (!strings.Contains(err.Error(), EnvStagingSite) || !strings.Contains(err.Error(), EnvStagingDomain)) {
			t.Errorf("%s: error does not name both required variables: %v", test.name, err)
		}
	}
}

func TestStagingFromValuesRejectsMalformedValues(t *testing.T) {
	const (
		validSite   = "https://login.staging.test"
		validDomain = "staging.test"
	)
	for _, test := range []struct {
		name   string
		site   string
		domain string
	}{
		{name: "empty site", domain: validDomain},
		{name: "bare hostname site", site: "login.staging.test", domain: validDomain},
		{name: "http scheme", site: "http://login.staging.test", domain: validDomain},
		{name: "port", site: "https://login.staging.test:8443", domain: validDomain},
		{name: "empty port", site: "https://login.staging.test:", domain: validDomain},
		{name: "path", site: "https://login.staging.test/login", domain: validDomain},
		// url.Parse drops these, so validateStagingSite needs its raw checks.
		{name: "empty query delimiter", site: "https://login.staging.test?", domain: validDomain},
		{name: "empty fragment delimiter", site: "https://login.staging.test#", domain: validDomain},
		{name: "percent-encoded path", site: "https://login.staging.test/%2f", domain: validDomain},
		{name: "credentials", site: "https://user:pass@login.staging.test", domain: validDomain},
		{name: "IPv4 site literal", site: "https://192.0.2.10", domain: validDomain},
		{name: "trailing dot site", site: "https://login.staging.test.", domain: validDomain},
		{name: "empty site label", site: "https://login..staging.test", domain: validDomain},
		{name: "leading hyphen site label", site: "https://-login.staging.test", domain: validDomain},
		{name: "underscore site label", site: "https://log_in.staging.test", domain: validDomain},
		{name: "api host as login origin", site: "https://api.staging.test", domain: validDomain},
		{name: "empty domain", site: validSite},
		{name: "single label domain", site: validSite, domain: "example"},
		{name: "scheme in domain", site: validSite, domain: "https://staging.test"},
		{name: "port in domain", site: validSite, domain: "staging.test:443"},
		{name: "path in domain", site: validSite, domain: "staging.test/api"},
		{name: "query in domain", site: validSite, domain: "staging.test?a=1"},
		{name: "leading dot domain", site: validSite, domain: ".staging.test"},
		{name: "trailing dot domain", site: validSite, domain: "staging.test."},
		{name: "empty domain label", site: validSite, domain: "staging..example.test"},
		{name: "wildcard domain", site: validSite, domain: "*.staging.test"},
		{name: "IPv4 domain literal", site: validSite, domain: "192.0.2.10"},
		{name: "underscore domain label", site: validSite, domain: "staging.ex_ample.test"},
	} {
		if _, ok, err := stagingFromValues(test.site, test.domain, "staging-test-client"); err == nil || ok {
			t.Errorf("%s: stagingFromValues(%q, %q) accepted", test.name, test.site, test.domain)
		}
	}

	// The standard 253-byte hostname limit applies to the whole name.
	longHost := strings.Repeat("ab.", 84) + "staging.test"
	if _, ok, err := stagingFromValues("https://"+longHost, validDomain, "staging-test-client"); err == nil || ok {
		t.Fatalf("stagingFromValues accepted a %d-byte hostname", len(longHost))
	}
}

func TestStagingFromValuesRejectsInvalidHostRelationships(t *testing.T) {
	for _, test := range []struct {
		name   string
		site   string
		domain string
	}{
		// Overlap with a built-in production family, in either direction.
		{name: "domain is a production family", site: "https://login.example.test", domain: "datadoghq.com"},
		{name: "domain is a production subdomain", site: "https://login.example.test", domain: "staging.datadoghq.eu"},
		{name: "domain is a production gov subdomain", site: "https://login.example.test", domain: "us.ddog-gov.com"},
		{name: "login host is a production family", site: "https://datadoghq.com", domain: "example.test"},
		{name: "login host is a production subdomain", site: "https://login.datadoghq.eu", domain: "example.test"},
		{name: "login host is a production gov subdomain", site: "https://login.ddog-gov.com", domain: "example.test"},
		// Login host outside the configured domain tree.
		{name: "sibling domain", site: "https://login.other.test", domain: "staging.test"},
		{name: "parent of domain", site: "https://login.example.test", domain: "staging.test"},
		{name: "unrelated host", site: "https://login.example.org", domain: "staging.test"},
		{name: "deep sibling under domain tree", site: "https://x.y.staging.test", domain: "other.test"},
	} {
		if _, ok, err := stagingFromValues(test.site, test.domain, "staging-test-client"); err == nil || ok {
			t.Errorf("%s: stagingFromValues(%q, %q) accepted", test.name, test.site, test.domain)
		}
	}
}

func TestStagingFromEnv(t *testing.T) {
	t.Setenv(EnvStagingSite, "https://login.staging.test")
	t.Setenv(EnvStagingDomain, "staging.test")
	t.Setenv(EnvStagingClientID, "staging-test-client")
	config, ok, err := StagingFromEnv()
	if err != nil {
		t.Fatalf("StagingFromEnv: %v", err)
	}
	if !ok || config.APIHost != "api.staging.test" {
		t.Fatalf("config = %#v, ok = %v", config, ok)
	}

	for name, values := range map[string][3]string{
		"partial":    {"https://login.staging.test", "", ""},
		"bad domain": {"https://login.staging.test", "staging..example.test", "client"},
	} {
		t.Setenv(EnvStagingSite, values[0])
		t.Setenv(EnvStagingDomain, values[1])
		t.Setenv(EnvStagingClientID, values[2])
		config, ok, err := StagingFromEnv()
		if err == nil || ok || config != (StagingConfig{}) {
			t.Errorf("%s: StagingFromEnv(%v) = %#v, %v, %v; want an error and no config", name, values, config, ok, err)
		}
	}

	t.Setenv(EnvStagingSite, " ")
	t.Setenv(EnvStagingDomain, "  ")
	t.Setenv(EnvStagingClientID, " ")
	config, ok, err = StagingFromEnv()
	if err != nil {
		t.Fatalf("StagingFromEnv with blank values: %v", err)
	}
	if ok || config != (StagingConfig{}) {
		t.Fatalf("config = %#v, ok = %v, want unset", config, ok)
	}
}

func TestStagingWebHostForIsExact(t *testing.T) {
	config, ok, err := stagingFromValues("https://login.staging.test", "staging.test", "")
	if err != nil || !ok {
		t.Fatalf("stagingFromValues: %v", err)
	}
	for host, wantOK := range map[string]bool{
		config.APIHost:   true,
		config.LoginHost: true,
		// Siblings, deeper children, and spoofs never map.
		"evil.staging.test":             false,
		"api.evil.staging.test":         false,
		"x.login.staging.test":          false,
		"staging.test":                  false,
		"login.staging.test.evil.test":  false,
		"api.datadoghq.com":             false,
		"api.staging.test.evil.example": false,
	} {
		got, gotOK := config.WebHostFor(host)
		if gotOK != wantOK || (wantOK && got != config.LoginHost) || (!wantOK && got != "") {
			t.Errorf("WebHostFor(%q) = %q, %v; want ok=%v mapping to login host", host, got, gotOK, wantOK)
		}
	}
}

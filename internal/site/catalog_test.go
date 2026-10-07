package site

import (
	"reflect"
	"testing"
)

func TestLoginRegions(t *testing.T) {
	got := LoginRegions()
	want := []Region{
		{Name: "US1", WebHost: "app.datadoghq.com"},
		{Name: "US3", WebHost: "us3.datadoghq.com"},
		{Name: "US5", WebHost: "us5.datadoghq.com"},
		{Name: "EU1", WebHost: "app.datadoghq.eu"},
		{Name: "AP1", WebHost: "ap1.datadoghq.com"},
		{Name: "AP2", WebHost: "ap2.datadoghq.com"},
		{Name: "UK1", WebHost: "uk1.datadoghq.com"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoginRegions() = %#v, want %#v", got, want)
	}
}

func TestWebHostForAssistantHost(t *testing.T) {
	for _, name := range []string{EnvStagingSite, EnvStagingDomain, EnvStagingClientID} {
		t.Setenv(name, "")
	}
	tests := []struct {
		host string
		want string
		ok   bool
	}{
		{host: "api.datadoghq.com", want: "app.datadoghq.com", ok: true},
		{host: "API.US3.DATADOGHQ.COM", want: "us3.datadoghq.com", ok: true},
		{host: "api.uk1.datadoghq.com", want: "uk1.datadoghq.com", ok: true},
		{host: "api.ddog-gov.com"},
		{host: "api.staging.test"},
	}
	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			got, ok := WebHostForAssistantHost(test.host)
			if got != test.want || ok != test.ok {
				t.Fatalf("WebHostForAssistantHost() = %q, %t, want %q, %t", got, ok, test.want, test.ok)
			}
		})
	}
}

// A configured staging environment maps its exact hosts and nothing else;
// a partial configuration leaves staging unmapped without touching production.
func TestWebHostForAssistantHostStagingEnvironment(t *testing.T) {
	t.Setenv(EnvStagingSite, "https://login.staging.test")
	t.Setenv(EnvStagingDomain, "staging.test")
	for host, want := range map[string]string{
		"api.staging.test":   "login.staging.test",
		"LOGIN.STAGING.TEST": "login.staging.test",
	} {
		if got, ok := WebHostForAssistantHost(host); !ok || got != want {
			t.Errorf("WebHostForAssistantHost(%q) = %q, %v; want %q", host, got, ok, want)
		}
	}
	for _, host := range []string{
		"evil.staging.test",
		"api.evil.staging.test",
		"x.login.staging.test",
		"staging.test",
	} {
		if got, ok := WebHostForAssistantHost(host); ok || got != "" {
			t.Errorf("WebHostForAssistantHost(%q) = %q, %v; want no mapping", host, got, ok)
		}
	}

	t.Setenv(EnvStagingDomain, "")
	if got, ok := WebHostForAssistantHost("login.staging.test"); ok || got != "" {
		t.Fatalf("partial staging environment mapped the login host to %q; want no mapping", got)
	}
	if got, ok := WebHostForAssistantHost("api.datadoghq.com"); !ok || got != "app.datadoghq.com" {
		t.Fatalf("production mapping broken by malformed staging env: %q, %v", got, ok)
	}
}

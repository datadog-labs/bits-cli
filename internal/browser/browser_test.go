package browser

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/datadog-labs/bits-cli/internal/site"
)

func TestOpenHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Open(ctx, "https://app.datadoghq.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context cancellation", err)
	}
}

func TestConversationURLUsesAuthoritativeSiteMapping(t *testing.T) {
	tests := []struct {
		name string
		site string
		want string
	}{
		{name: "US1", site: "https://api.datadoghq.com", want: "https://app.datadoghq.com/ask/conversation-1"},
		{name: "US3", site: "https://api.us3.datadoghq.com", want: "https://us3.datadoghq.com/ask/conversation-1"},
		{name: "US5", site: "https://api.us5.datadoghq.com", want: "https://us5.datadoghq.com/ask/conversation-1"},
		{name: "EU1", site: "https://api.datadoghq.eu", want: "https://app.datadoghq.eu/ask/conversation-1"},
		{name: "AP1", site: "https://api.ap1.datadoghq.com", want: "https://ap1.datadoghq.com/ask/conversation-1"},
		{name: "AP2", site: "https://api.ap2.datadoghq.com", want: "https://ap2.datadoghq.com/ask/conversation-1"},
		{name: "UK1", site: "https://api.uk1.datadoghq.com", want: "https://uk1.datadoghq.com/ask/conversation-1"},
	}
	for _, name := range []string{site.EnvStagingSite, site.EnvStagingDomain, site.EnvStagingClientID} {
		t.Setenv(name, "")
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ConversationURL(test.site, "conversation-1")
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ConversationURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSettingsURLUsesAuthoritativeSiteMapping(t *testing.T) {
	tests := []struct {
		name string
		site string
		want string
	}{
		{name: "US1", site: "https://api.datadoghq.com", want: "https://app.datadoghq.com/ask/settings"},
		{name: "US3", site: "https://api.us3.datadoghq.com", want: "https://us3.datadoghq.com/ask/settings"},
		{name: "EU1", site: "https://api.datadoghq.eu", want: "https://app.datadoghq.eu/ask/settings"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SettingsURL(test.site)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("SettingsURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSettingsURLRejectsUnsupportedSite(t *testing.T) {
	for _, name := range []string{site.EnvStagingSite, site.EnvStagingDomain, site.EnvStagingClientID} {
		t.Setenv(name, "")
	}
	if got, err := SettingsURL("https://api.ddog-gov.com"); err == nil || got != "" {
		t.Fatalf("SettingsURL() = %q, %v, want an error", got, err)
	}
	if got, err := SettingsURL("https://api.staging.test"); err == nil || got != "" {
		t.Fatalf("SettingsURL() without staging configuration = %q, %v, want an error", got, err)
	}
}

// A staging environment maps exactly its configured hosts to the login
// origin; a partial configuration never breaks production mapping.
func TestConversationURLStagingEnvironment(t *testing.T) {
	t.Setenv(site.EnvStagingSite, "https://login.staging.test")
	t.Setenv(site.EnvStagingDomain, "staging.test")
	for siteURL, want := range map[string]string{
		"https://api.staging.test":   "https://login.staging.test/ask/conversation-1",
		"https://login.staging.test": "https://login.staging.test/ask/conversation-1",
	} {
		if got, err := ConversationURL(siteURL, "conversation-1"); err != nil || got != want {
			t.Errorf("ConversationURL(%q) = %q, %v; want %q", siteURL, got, err, want)
		}
	}
	for _, siteURL := range []string{
		"https://evil.staging.test",
		"https://api.evil.staging.test",
		"https://x.login.staging.test",
		"https://staging.test",
		"https://login.staging.test.evil.example",
	} {
		if got, err := ConversationURL(siteURL, "conversation-1"); err == nil || got != "" {
			t.Errorf("ConversationURL(%q) = %q, %v; want an error", siteURL, got, err)
		}
	}

	t.Setenv(site.EnvStagingDomain, "")
	if got, err := ConversationURL("https://api.staging.test", "conversation-1"); err == nil || got != "" {
		t.Fatalf("partial staging configuration: ConversationURL = %q, %v; want an error", got, err)
	}
	got, err := ConversationURL("https://api.datadoghq.com", "conversation-1")
	if err != nil || got != "https://app.datadoghq.com/ask/conversation-1" {
		t.Fatalf("production mapping broken by malformed staging env: %q, %v", got, err)
	}
}

func TestConversationURLEncodesConversationID(t *testing.T) {
	got, err := ConversationURL("https://api.datadoghq.com", " conversation/id?copy=yes ")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://app.datadoghq.com/ask/conversation%2Fid%3Fcopy=yes"
	if got != want {
		t.Fatalf("ConversationURL() = %q, want %q", got, want)
	}
}

func TestConversationURLRejectsMissingIDAndUnsupportedSites(t *testing.T) {
	tests := []struct {
		name string
		site string
		id   string
	}{
		{name: "missing ID", site: "https://api.datadoghq.com"},
		{name: "empty site", id: "conversation-1"},
		{name: "unknown host", site: "https://example.com", id: "conversation-1"},
		{name: "GovCloud", site: "https://api.ddog-gov.com", id: "conversation-1"},
		{name: "customer subdomain", site: "https://api.acme.us3.datadoghq.com", id: "conversation-1"},
		{name: "HTTP", site: "http://api.datadoghq.com", id: "conversation-1"},
		{name: "path", site: "https://api.datadoghq.com/other", id: "conversation-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := ConversationURL(test.site, test.id); err == nil || got != "" {
				t.Fatalf("ConversationURL() = %q, %v, want an error", got, err)
			}
		})
	}
}

func TestLauncherCommand(t *testing.T) {
	target := "https://app.datadoghq.com/ask/conversation-1"
	tests := []struct {
		goos    string
		command string
		args    []string
	}{
		{goos: "darwin", command: "open", args: []string{target}},
		{goos: "windows", command: "rundll32", args: []string{"url.dll,FileProtocolHandler", target}},
		{goos: "linux", command: "xdg-open", args: []string{target}},
		{goos: "freebsd", command: "xdg-open", args: []string{target}},
	}
	for _, test := range tests {
		t.Run(test.goos, func(t *testing.T) {
			command, args := launcherCommand(test.goos, target)
			if command != test.command || !reflect.DeepEqual(args, test.args) {
				t.Fatalf("launcherCommand() = %q, %v, want %q, %v", command, args, test.command, test.args)
			}
			if strings.Join(args, " ") == "" {
				t.Fatal("launcher omitted the target")
			}
		})
	}
}

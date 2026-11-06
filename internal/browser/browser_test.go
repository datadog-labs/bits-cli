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

func TestWebOriginUsesAuthoritativeSiteMapping(t *testing.T) {
	tests := []struct {
		name string
		site string
		want string
	}{
		{name: "US1", site: "https://api.datadoghq.com", want: "https://app.datadoghq.com"},
		{name: "US3", site: "https://api.us3.datadoghq.com", want: "https://us3.datadoghq.com"},
		{name: "US5", site: "https://api.us5.datadoghq.com", want: "https://us5.datadoghq.com"},
		{name: "EU1", site: "https://api.datadoghq.eu", want: "https://app.datadoghq.eu"},
		{name: "AP1", site: "https://api.ap1.datadoghq.com", want: "https://ap1.datadoghq.com"},
		{name: "AP2", site: "https://api.ap2.datadoghq.com", want: "https://ap2.datadoghq.com"},
		{name: "UK1", site: "https://api.uk1.datadoghq.com", want: "https://uk1.datadoghq.com"},
	}
	for _, name := range []string{site.EnvStagingSite, site.EnvStagingDomain, site.EnvStagingClientID} {
		t.Setenv(name, "")
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := WebOrigin(test.site)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("WebOrigin() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWebOriginRejectsUnsupportedSite(t *testing.T) {
	for _, name := range []string{site.EnvStagingSite, site.EnvStagingDomain, site.EnvStagingClientID} {
		t.Setenv(name, "")
	}
	if got, err := WebOrigin("https://api.ddog-gov.com"); err == nil || got != "" {
		t.Fatalf("WebOrigin() = %q, %v, want an error", got, err)
	}
	if got, err := WebOrigin("https://api.staging.test"); err == nil || got != "" {
		t.Fatalf("WebOrigin() without staging configuration = %q, %v, want an error", got, err)
	}
}

// A staging environment maps exactly its configured hosts to the login
// origin; a partial configuration never breaks production mapping.
func TestWebOriginStagingEnvironment(t *testing.T) {
	t.Setenv(site.EnvStagingSite, "https://login.staging.test")
	t.Setenv(site.EnvStagingDomain, "staging.test")
	for siteURL, want := range map[string]string{
		"https://api.staging.test":   "https://login.staging.test",
		"https://login.staging.test": "https://login.staging.test",
	} {
		if got, err := WebOrigin(siteURL); err != nil || got != want {
			t.Errorf("WebOrigin(%q) = %q, %v; want %q", siteURL, got, err, want)
		}
	}
	for _, siteURL := range []string{
		"https://evil.staging.test",
		"https://api.evil.staging.test",
		"https://x.login.staging.test",
		"https://staging.test",
		"https://login.staging.test.evil.example",
	} {
		if got, err := WebOrigin(siteURL); err == nil || got != "" {
			t.Errorf("WebOrigin(%q) = %q, %v; want an error", siteURL, got, err)
		}
	}

	t.Setenv(site.EnvStagingDomain, "")
	if got, err := WebOrigin("https://api.staging.test"); err == nil || got != "" {
		t.Fatalf("partial staging configuration: WebOrigin = %q, %v; want an error", got, err)
	}
	got, err := WebOrigin("https://api.datadoghq.com")
	if err != nil || got != "https://app.datadoghq.com" {
		t.Fatalf("production mapping broken by malformed staging env: %q, %v", got, err)
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

// Without a saved org origin, staging links fall back to the currently
// configured login origin.
func TestWebOriginStagingLoginAliasChange(t *testing.T) {
	t.Setenv(site.EnvStagingSite, "https://login.staging.test")
	t.Setenv(site.EnvStagingDomain, "staging.test")
	t.Setenv(site.EnvStagingClientID, "")
	got, err := WebOrigin("https://api.staging.test")
	if err != nil || got != "https://login.staging.test" {
		t.Fatalf("WebOrigin = %q, %v", got, err)
	}

	t.Setenv(site.EnvStagingSite, "https://ui.staging.test")
	got, err = WebOrigin("https://api.staging.test")
	if err != nil || got != "https://ui.staging.test" {
		t.Fatalf("WebOrigin after alias change = %q, %v", got, err)
	}
}

func TestConversationURLEncodesConversationID(t *testing.T) {
	got, err := ConversationURL("https://demo.datadoghq.com", " conversation/id?copy=yes ")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://demo.datadoghq.com/ask/conversation%2Fid%3Fcopy=yes"
	if got != want {
		t.Fatalf("ConversationURL() = %q, want %q", got, want)
	}
}

func TestLinksUseResolvedOrigin(t *testing.T) {
	for _, origin := range []string{"https://demo.datadoghq.com", "https://acme.us3.datadoghq.com", "https://acme.staging.test"} {
		got, err := ConversationURL(origin, "conversation-1")
		if err != nil || got != origin+"/ask/conversation-1" {
			t.Fatalf("conversation = %q, %v", got, err)
		}
		got, err = SettingsURL(origin)
		if err != nil || got != origin+"/ask/settings" {
			t.Fatalf("settings = %q, %v", got, err)
		}
	}
	if _, err := ConversationURL("", "id"); err == nil {
		t.Fatal("accepted empty origin")
	}
	if _, err := ConversationURL("https://demo.datadoghq.com", " "); err == nil {
		t.Fatal("accepted empty ID")
	}
	if _, err := SettingsURL(""); err == nil {
		t.Fatal("accepted empty origin")
	}
}

package browser

import (
	"reflect"
	"strings"
	"testing"
)

func TestConversationURLUsesAuthoritativeSiteMapping(t *testing.T) {
	tests := []struct {
		name string
		site string
		want string
	}{
		{name: "US1", site: "https://api.datadoghq.com", want: "https://app.datadoghq.com/bits?conversation_id=conversation-1"},
		{name: "US3", site: "https://api.us3.datadoghq.com", want: "https://us3.datadoghq.com/bits?conversation_id=conversation-1"},
		{name: "US5", site: "https://api.us5.datadoghq.com", want: "https://us5.datadoghq.com/bits?conversation_id=conversation-1"},
		{name: "EU1", site: "https://api.datadoghq.eu", want: "https://app.datadoghq.eu/bits?conversation_id=conversation-1"},
		{name: "AP1", site: "https://api.ap1.datadoghq.com", want: "https://ap1.datadoghq.com/bits?conversation_id=conversation-1"},
		{name: "AP2", site: "https://api.ap2.datadoghq.com", want: "https://ap2.datadoghq.com/bits?conversation_id=conversation-1"},
		{name: "staging API", site: "https://api.datad0g.com", want: "https://dd.datad0g.com/bits?conversation_id=conversation-1"},
		{name: "staging direct", site: "https://dd.datad0g.com", want: "https://dd.datad0g.com/bits?conversation_id=conversation-1"},
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

func TestConversationURLEncodesConversationID(t *testing.T) {
	got, err := ConversationURL("https://api.datadoghq.com", " conversation/id?copy=yes ")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://app.datadoghq.com/bits?conversation_id=conversation%2Fid%3Fcopy%3Dyes"
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
	target := "https://app.datadoghq.com/bits?conversation_id=conversation-1"
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

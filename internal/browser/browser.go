// Package browser builds Datadog web links and opens them with the platform's
// browser launcher.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const openTimeout = 10 * time.Second

var conversationHosts = map[string]string{
	"api.datadoghq.com":     "app.datadoghq.com",
	"api.us3.datadoghq.com": "us3.datadoghq.com",
	"api.us5.datadoghq.com": "us5.datadoghq.com",
	"api.datadoghq.eu":      "app.datadoghq.eu",
	"api.ap1.datadoghq.com": "ap1.datadoghq.com",
	"api.ap2.datadoghq.com": "ap2.datadoghq.com",
	"api.datad0g.com":       "dd.datad0g.com",
	"dd.datad0g.com":        "dd.datad0g.com",
}

// ConversationURL returns the dedicated Bits web page for a conversation on
// the Datadog site serving the Assistant API. The explicit mapping is the
// supported-site boundary; guessing by hostname risks opening another region.
func ConversationURL(apiSite, conversationID string) (string, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", errors.New("conversation ID is empty")
	}

	site, err := url.Parse(strings.TrimSpace(apiSite))
	if err != nil || site.Scheme != "https" || site.Hostname() == "" || site.Host != site.Hostname() ||
		site.User != nil || site.RawQuery != "" || site.Fragment != "" || (site.Path != "" && site.Path != "/") {
		return "", fmt.Errorf("unsupported Datadog Assistant site %q", apiSite)
	}
	webHost, ok := conversationHosts[strings.ToLower(site.Hostname())]
	if !ok {
		return "", fmt.Errorf("unsupported Datadog Assistant site %q", apiSite)
	}

	target := &url.URL{
		Scheme:  "https",
		Host:    webHost,
		Path:    "/ask/" + conversationID,
		RawPath: "/ask/" + url.PathEscape(conversationID),
	}
	return target.String(), nil
}

// Open starts the operating system's browser launcher and waits long enough to
// observe startup failures. Callers should retain the URL as a manual fallback.
func Open(ctx context.Context, target string) error {
	command, args := launcherCommand(runtime.GOOS, target)
	launchCtx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()
	return exec.CommandContext(launchCtx, command, args...).Run()
}

func launcherCommand(goos, target string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{target}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		return "xdg-open", []string{target}
	}
}

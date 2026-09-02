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

	"github.com/DataDog/bits-cli/internal/site"
)

const openTimeout = 10 * time.Second

// ConversationURL returns the dedicated Bits web page for a conversation on
// the Datadog site serving the Assistant API. The explicit mapping is the
// supported-site boundary; guessing by hostname risks opening another region.
func ConversationURL(apiSite, conversationID string) (string, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", errors.New("conversation ID is empty")
	}

	parsedSite, err := url.Parse(strings.TrimSpace(apiSite))
	if err != nil || parsedSite.Scheme != "https" || parsedSite.Hostname() == "" || parsedSite.Host != parsedSite.Hostname() ||
		parsedSite.User != nil || parsedSite.RawQuery != "" || parsedSite.Fragment != "" || (parsedSite.Path != "" && parsedSite.Path != "/") {
		return "", fmt.Errorf("unsupported Datadog Assistant site %q", apiSite)
	}
	webHost, ok := site.WebHostForAssistantHost(parsedSite.Hostname())
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

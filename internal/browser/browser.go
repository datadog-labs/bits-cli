// Package browser builds Datadog web links and opens them with the platform's
// browser launcher.
package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/datadog-labs/bits-cli/internal/site"
)

const openTimeout = 10 * time.Second

// ConversationURL returns the dedicated Bits web page for a conversation on
// the resolved Datadog web origin.
func ConversationURL(origin, conversationID string) (string, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", errors.New("conversation ID is empty")
	}

	if origin == "" {
		return "", errors.New("web origin is empty")
	}
	return origin + "/ask/" + url.PathEscape(conversationID), nil
}

// SettingsURL returns the Assistant settings page on the resolved web origin.
func SettingsURL(origin string) (string, error) {
	if origin == "" {
		return "", errors.New("web origin is empty")
	}
	return origin + "/bits/settings", nil
}

// Links resolves the scheme-less links the Assistant writes, which name
// either a Datadog app route or a workspace file, into links that open
// outside a web page.
type Links struct {
	Origin    string // Datadog web origin, "" if unknown
	Workspace string // absolute workspace path, "" if none
}

// Resolve returns ref as a file:// link when it names a path in the
// workspace, where the agent works on files: relative to it, the form the
// file tools take, or absolute. Any other root-relative ref is an app route
// on the origin. Other refs, and refs that do not parse, are unchanged.
// NOTE: an absolute path outside the workspace, such as /tmp/out.log, reads as
// an app route; the model links such files as file:// URIs.
func (l Links) Resolve(ref string) string {
	// A Windows path such as C:/repo/a.go would otherwise parse with scheme c.
	volume := filepath.VolumeName(ref) != ""
	target := ref
	if volume {
		target = fileURLPath(ref)
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
		return ref
	}
	ws := strings.TrimSuffix(fileURLPath(l.Workspace), "/")
	if !strings.HasPrefix(ws, "/") {
		ws = ""
	}
	file := filepath.ToSlash(u.Path)
	relative := !strings.HasPrefix(file, "/")
	if relative && ws == "" {
		return ref
	}
	if relative {
		file = ws + "/" + file
	}
	// Cleaning first keeps ../ segments from leaving the workspace.
	if file = path.Clean(file); ws != "" && strings.HasPrefix(file, ws+"/") {
		// A file opens whole: terminals such as Rex reject a file:// link
		// with a line anchor, and no editor reads one from it.
		return (&url.URL{Scheme: "file", Path: file}).String()
	}
	if relative || volume {
		return ref
	}
	origin, err := url.Parse(l.Origin)
	if err != nil || origin.Host == "" {
		return ref
	}
	u.Scheme, u.Host = origin.Scheme, origin.Host
	return u.String()
}

// fileURLPath returns the path of the file:// URL for name: slash-separated,
// with a leading slash before a Windows volume (file:///C:/repo).
func fileURLPath(name string) string {
	if filepath.VolumeName(name) != "" {
		return "/" + filepath.ToSlash(name)
	}
	return filepath.ToSlash(name)
}

// WebOrigin returns the Datadog web app origin for the site serving the
// Assistant API.
func WebOrigin(apiSite string) (string, error) {
	webHost, err := webHostForAssistantSite(apiSite)
	if err != nil {
		return "", err
	}
	return (&url.URL{Scheme: "https", Host: webHost}).String(), nil
}

func webHostForAssistantSite(apiSite string) (string, error) {
	parsedSite, err := url.Parse(strings.TrimSpace(apiSite))
	if err != nil || parsedSite.Scheme != "https" || parsedSite.Hostname() == "" || parsedSite.Host != parsedSite.Hostname() ||
		parsedSite.User != nil || parsedSite.RawQuery != "" || parsedSite.Fragment != "" || (parsedSite.Path != "" && parsedSite.Path != "/") {
		return "", fmt.Errorf("unsupported Datadog Assistant site %q", apiSite)
	}
	webHost, ok := site.WebHostForAssistantHost(parsedSite.Hostname())
	if !ok {
		return "", fmt.Errorf("unsupported Datadog Assistant site %q", apiSite)
	}
	return webHost, nil
}

// Open starts the operating system's browser launcher and waits long enough to
// observe startup failures. Callers should retain the URL as a manual fallback.
func Open(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

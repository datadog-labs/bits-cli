package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/datadog-labs/bits-cli/internal/version"
	"golang.org/x/mod/semver"
)

var releasesURL = "https://github.com/datadog-labs/bits-cli/releases"

// An installation keeps this URL for its lifetime. The latest installer owns
// archive formats and migrations, so released updaters need not understand them.
var installerBaseURL = "https://raw.githubusercontent.com/datadog-labs/bits-cli/HEAD/scripts"

// standalone updates an installation of the install scripts from GitHub
// releases, in the layout the installers maintain.
type standalone struct {
	// check makes the one request that finds the latest version.
	check *http.Client
	// download fetches the complete current installer.
	download  *http.Client
	url       string
	root      string
	current   string
	installer string
}

func newStandalone(url, root, current string) standalone {
	name := "/install.sh"
	if runtime.GOOS == "windows" {
		name = "/install.ps1"
	}
	return standalone{
		url:       url,
		root:      root,
		current:   current,
		installer: installerBaseURL + name,
		check: &http.Client{
			Timeout: 5 * time.Second,
			// /latest redirects to the tag page, whose URL names the version.
			// A renamed or transferred repository redirects first.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 3 || req.URL.Host != via[0].URL.Host {
					return fmt.Errorf("unexpected redirect to %s", req.URL)
				}
				if err := keepHTTPS(req, via); err != nil {
					return err
				}
				if strings.Contains(req.URL.Path, "/releases/tag/") {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
		download: &http.Client{
			// Long enough for a slow link, so a stalled one cannot hang bits update.
			Timeout: 10 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("stopped after 10 redirects")
				}
				return keepHTTPS(req, via)
			},
		},
	}
}

// keepHTTPS refuses a redirect from HTTPS to anything else.
func keepHTTPS(req *http.Request, via []*http.Request) error {
	if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect to %s", req.URL)
	}
	return nil
}

func detectStandalone(executable, buildVersion string) (backend, error) {
	resolved, err := resolveExecutable(executable)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve executable: %w", ErrUnsupportedInstallation, err)
	}
	bin := filepath.Dir(resolved)
	dir := filepath.Dir(bin)
	versions := filepath.Dir(dir)
	v := filepath.Base(dir)
	if filepath.Base(resolved) != executableName || filepath.Base(bin) != "bin" || filepath.Base(versions) != "versions" || !version.IsStable(v) || !version.IsStable(buildVersion) {
		return nil, ErrUnsupportedInstallation
	}
	root := filepath.Dir(versions)
	_, err = os.Stat(filepath.Join(dir, ".install-complete"))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedInstallation, err)
	}
	// A link can be repointed after process launch. Its resolved target locates
	// the installation, while the running build supplies the stale-update guard.
	return newStandalone(releasesURL, root, buildVersion), nil
}

func (standalone) Name() string { return "standalone" }

func (s standalone) LatestVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.url+"/latest", nil)
	if err != nil {
		return "", fmt.Errorf("create release request: %w", err)
	}
	resp, err := s.check.Do(req)
	if err != nil {
		return "", fmt.Errorf("find the latest release: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return "", err
	}
	location, err := resp.Location()
	if err != nil {
		return "", fmt.Errorf("find the latest release: HTTP %d", resp.StatusCode)
	}
	dir, tag := path.Split(location.Path)
	if !strings.HasSuffix(dir, "/releases/tag/") || !version.IsStable(tag) {
		return "", fmt.Errorf("%w: latest release is %s", errInvalidVersion, location)
	}
	return tag, nil
}

// Upgrade runs the complete current installer with an explicit installation root
// and target. Environment changes since installation cannot move or pin an update.
func (s standalone) Upgrade(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) (err error) {
	latest, err := s.LatestVersion(ctx)
	if err != nil {
		return err
	}
	if semver.Compare(latest, s.current) <= 0 {
		_, err := fmt.Fprintf(stderr, "Bits %s is up to date.\n", s.current)
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.installer, nil)
	if err != nil {
		return err
	}
	resp, err := s.download.Do(req)
	if err != nil {
		return fmt.Errorf("download installer: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download installer: HTTP %d", resp.StatusCode)
	}
	dir, err := os.MkdirTemp("", "bits-installer-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	name := "install.sh"
	if runtime.GOOS == "windows" {
		name = "install.ps1"
	}
	script := filepath.Join(dir, name)
	file, err := os.OpenFile(script, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, resp.Body)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return fmt.Errorf("download installer: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-Root", s.root, "-Version", latest, "-UpdateFrom", s.current)
	} else {
		cmd = exec.Command("sh", script, "--root", s.root, "--version", latest, "--update-from", s.current)
	}
	// Scripts stage before their atomic switch. Stop the entire process tree on
	// cancellation so a download cannot finish and activate after Ctrl-C.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = append(os.Environ(), "BITS_RELEASES_URL="+s.url)
	if err := runInstaller(ctx, cmd); err != nil {
		return fmt.Errorf("run installer: %w", err)
	}
	return nil
}

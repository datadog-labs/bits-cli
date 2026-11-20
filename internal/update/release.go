package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const releasesURL = "https://github.com/datadog-labs/bits-cli/releases"

// standalone updates an installation of the install scripts from GitHub
// releases, in the layout Install maintains.
type standalone struct {
	// check makes the one request that finds the latest version.
	check *http.Client
	// download fetches release assets through GitHub's CDN redirects.
	download *http.Client
	url      string
	root     string
	current  string
}

func newStandalone(url, root, current string) standalone {
	return standalone{
		url:     url,
		root:    root,
		current: current,
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

func detectStandalone(executable string) (backend, error) {
	root, v, err := standaloneVersion(executable)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnsupportedInstallation, err)
	}
	return newStandalone(releasesURL, root, v), nil
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
	_ = resp.Body.Close()
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

// Upgrade unpacks the latest release next to the running one and runs its
// bits install, which switches to it. An interrupted upgrade leaves only a
// staging directory, which a later install removes.
func (s standalone) Upgrade(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	latest, err := s.LatestVersion(ctx)
	if err != nil {
		return err
	}
	if semver.Compare(latest, s.current) <= 0 {
		_, err := fmt.Fprintf(stderr, "Bits %s is up to date.\n", s.current)
		return err
	}
	_, _ = fmt.Fprintf(stderr, "Updating Bits to %s\n", latest)
	executable, err := s.unpack(ctx, latest)
	if err != nil {
		return fmt.Errorf("download Bits %s: %w", latest, err)
	}
	// A cancel up to here leaves the release unpacked but inactive. bits
	// install then runs to the end: it catches Ctrl-C as this process does,
	// and its switch is atomic.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("update Bits to %s: %w", latest, err)
	}
	cmd := exec.Command(executable, "install")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s install: %w", executable, err)
	}
	return nil
}

// unpack downloads and verifies the release archive for this platform and
// moves it, unpacked and marked complete, to versions/<v>. It returns the
// executable there.
func (s standalone) unpack(ctx context.Context, v string) (_ string, err error) {
	dir := filepath.Join(s.root, versionsDir, v)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(s.root, stagingPrefix+"*")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(staging)) }()

	archive := archiveName(v)
	sums, err := s.get(ctx, v, checksumsName(v))
	if err != nil {
		return "", err
	}
	want, err := checksum(sums, archive)
	if err != nil {
		return "", err
	}
	content, err := s.get(ctx, v, archive)
	if err != nil {
		return "", err
	}
	if got := sha256.Sum256(content); !bytes.Equal(got[:], want) {
		return "", fmt.Errorf("%s does not match its checksum", archive)
	}
	prefix := filepath.Join(staging, "prefix")
	if err := extract(archive, content, prefix); err != nil {
		return "", fmt.Errorf("unpack %s: %w", archive, err)
	}
	if err := os.WriteFile(filepath.Join(prefix, completeMarker), nil, 0o644); err != nil {
		return "", err
	}
	if err := place(prefix, dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "bin", executableName), nil
}

// get reads a release asset of tag v. Assets are a few megabytes, so they are
// held in memory and verified before anything is written.
func (s standalone) get(ctx context.Context, v, name string) ([]byte, error) {
	url := s.url + "/download/" + v + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.download.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func archiveName(v string) string {
	name := fmt.Sprintf("bits_%s_%s_%s", strings.TrimPrefix(v, "v"), runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

func checksumsName(v string) string {
	return fmt.Sprintf("bits_%s_checksums.txt", strings.TrimPrefix(v, "v"))
}

// checksum finds the SHA-256 of name in sha256sum output.
func checksum(sums []byte, name string) ([]byte, error) {
	for line := range strings.Lines(string(sums)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return hex.DecodeString(fields[0])
		}
	}
	return nil, fmt.Errorf("no checksum for %s", name)
}

// extract unpacks a release archive into dir, which must not exist. os.Root
// keeps every entry inside it.
func extract(name string, content []byte, dir string) error {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if strings.HasSuffix(name, ".zip") {
		return extractZip(content, root)
	}
	return extractTarGz(content, root)
}

func extractTarGz(content []byte, root *os.Root) error {
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return err
	}
	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(filepath.FromSlash(header.Name), 0o755)
		case tar.TypeReg:
			err = writeFile(root, header.Name, header.FileInfo().Mode().Perm(), archive)
		default:
			err = fmt.Errorf("unexpected entry %s", header.Name)
		}
		if err != nil {
			return err
		}
	}
}

func extractZip(content []byte, root *os.Root) error {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return err
	}
	for _, file := range archive.File {
		if file.FileInfo().IsDir() {
			if err := root.MkdirAll(filepath.FromSlash(file.Name), 0o755); err != nil {
				return err
			}
			continue
		}
		r, err := file.Open()
		if err != nil {
			return err
		}
		err = writeFile(root, file.Name, file.Mode().Perm(), r)
		if closeErr := r.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeFile(root *os.Root, name string, perm os.FileMode, r io.Reader) error {
	name = filepath.FromSlash(name)
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		return errors.Join(err, f.Close())
	}
	return f.Close()
}

// place moves an unpacked prefix to dir. A version directory only appears
// complete, by this rename, and leaves by a rename too, so a dir that exists
// is this version from an earlier or concurrent install, which may be running.
func place(prefix, dir string) error {
	err := os.Rename(prefix, dir)
	if err == nil || complete(dir) {
		return nil
	}
	if _, statErr := os.Stat(dir); statErr == nil {
		return fmt.Errorf("%s is incomplete; remove it and try again", dir)
	}
	return err
}

func complete(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, completeMarker))
	return err == nil
}

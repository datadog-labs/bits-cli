package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/mod/semver"
)

type homebrew struct {
	client *http.Client
	url    string
}

func newHomebrew(url string) homebrew {
	return homebrew{
		url: url,
		client: &http.Client{
			Timeout: 5 * time.Second,
			// Keep each check to one HTTP request.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (homebrew) Name() string { return "homebrew" }

func (h homebrew) LatestVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.url, nil)
	if err != nil {
		return "", fmt.Errorf("create formula request: %w", err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch formula: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch formula: HTTP %d", resp.StatusCode)
	}
	const maxFormulaSize = 64 * 1024
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFormulaSize+1))
	if err != nil {
		return "", fmt.Errorf("read formula: %w", err)
	}
	if len(raw) > maxFormulaSize {
		return "", fmt.Errorf("update formula is too large")
	}
	return homebrewVersion(raw)
}

// detectBackend uses the running executable, not whichever bits or brew happens
// to be on PATH. The keg layout also works with non-default Homebrew prefixes.
func detectBackend(executable string) (backend, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve executable: %w", ErrUnsupportedInstallation, err)
	}
	bin := filepath.Dir(resolved)
	keg := filepath.Dir(bin)
	rack := filepath.Dir(keg)
	if filepath.Base(resolved) != "bits" || filepath.Base(bin) != "bin" ||
		filepath.Base(rack) != "bits" || filepath.Base(filepath.Dir(rack)) != "Cellar" {
		return nil, ErrUnsupportedInstallation
	}
	raw, err := os.ReadFile(filepath.Join(keg, "INSTALL_RECEIPT.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: read Homebrew receipt: %w", ErrUnsupportedInstallation, err)
	}
	var receipt struct {
		Source struct {
			Tap string `json:"tap"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, fmt.Errorf("%w: decode Homebrew receipt: %w", ErrUnsupportedInstallation, err)
	}
	if receipt.Source.Tap != "datadog-labs/pack" {
		return nil, fmt.Errorf("%w: Homebrew tap %q", ErrUnsupportedInstallation, receipt.Source.Tap)
	}
	return newHomebrew("https://raw.githubusercontent.com/datadog-labs/homebrew-pack/main/Formula/bits.rb"), nil
}

// Read only the explicit version declaration in our generated formula. This is
// deliberately not a Ruby parser; unexpected formats fail the check silently.
var formulaVersion = regexp.MustCompile(`(?m)^[\t ]*version[\t ]+"([^"\r\n]+)"[\t ]*\r?$`)

func homebrewVersion(raw []byte) (string, error) {
	matches := formulaVersion.FindAllSubmatch(raw, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf("%w: expected one explicit Homebrew version", ErrInvalidVersion)
	}
	latest := "v" + string(matches[0][1])
	if !semver.IsValid(latest) {
		return "", fmt.Errorf("%w: Homebrew version %q", ErrInvalidVersion, latest)
	}
	return latest, nil
}

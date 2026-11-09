package assistant

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

// SnapshotOptions describes one server-side widget rendering.
type SnapshotOptions struct {
	Definition    WidgetDefinition
	Start, End    time.Time
	Width, Height int // CSS pixels; the PNG is four times larger
	Dark          bool
}

// maxSnapshotBytes bounds a PNG; previews are well under 1 MiB.
const maxSnapshotBytes = 32 << 20

// snapshotView is the only path a view URL may name. Requiring the decoded
// path to match, with no escapes, keeps a response from steering the
// authenticated GET to another endpoint.
var snapshotView = regexp.MustCompile(`^/api/v2/snapshot/view/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*/[A-Za-z0-9_.-]+\.png$`)

var errSnapshotPending = errors.New("snapshot pending")

type snapshotRequest struct {
	Data snapshotRequestData `json:"data"`
}

type snapshotRequestData struct {
	Type       string                    `json:"type"`
	Attributes snapshotRequestAttributes `json:"attributes"`
}

type snapshotRequestAttributes struct {
	Definition    WidgetDefinition `json:"widget_definition"`
	Start         int64            `json:"start"` // Unix milliseconds
	End           int64            `json:"end"`
	Width         int              `json:"width"`
	Height        int              `json:"height"`
	Authenticated bool             `json:"is_authenticated"`
	TTL           string           `json:"ttl"`
	Config        snapshotConfig   `json:"additional_config"`
}

type snapshotConfig struct {
	Theme    string `json:"theme"`
	TZOffset *int   `json:"timezone_offset_minutes,omitempty"` // positive west of UTC, as in JavaScript
}

// RenderSnapshot renders a widget with Datadog's snapshot service and returns
// the PNG. Only creation errors are typed: a view that fails says nothing
// about whether this login may create snapshots.
func (c *Client) RenderSnapshot(ctx context.Context, o SnapshotOptions) ([]byte, error) {
	path, err := c.createSnapshot(ctx, o)
	if err != nil {
		return nil, err
	}
	for delay := 500 * time.Millisecond; ; delay = min(2*delay, 2*time.Second) {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		data, err := c.snapshotView(ctx, path)
		switch {
		case err == nil:
			return data, nil
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case !errors.Is(err, errSnapshotPending):
			return nil, fmt.Errorf("snapshot view: %v", err) //nolint:errorlint // deliberately untyped, see above
		}
	}
}

// createSnapshot is a POST, so do never retries it: creation has no
// idempotency key.
func (c *Client) createSnapshot(ctx context.Context, o SnapshotOptions) (string, error) {
	theme := "light"
	if o.Dark {
		theme = "dark"
	}
	_, offset := o.End.Zone()
	// The snapshot API currently rejects offsets below -12 hours, including
	// valid UTC+13 and UTC+14 zones. Omit those offsets so rendering can proceed.
	tzOffset := -offset / 60
	var acceptedOffset *int
	if tzOffset >= -12*60 && tzOffset <= 14*60 {
		acceptedOffset = &tzOffset
	}
	req := snapshotRequest{Data: snapshotRequestData{
		Type: "create_snapshot",
		Attributes: snapshotRequestAttributes{
			Definition:    o.Definition,
			Start:         o.Start.UnixMilli(),
			End:           o.End.UnixMilli(),
			Width:         o.Width,
			Height:        o.Height,
			Authenticated: true,
			TTL:           "30d", // the shortest retention offered
			Config:        snapshotConfig{Theme: theme, TZOffset: acceptedOffset},
		},
	}}
	resp, err := c.do(ctx, http.MethodPost, "/api/v2/snapshot", req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Data struct {
			Attributes struct {
				URL string `json:"url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := decodeJSONBody(&out, resp.Body, maxResponseBodyBytes); err != nil {
		return "", fmt.Errorf("decode snapshot: %w", err)
	}
	// The URL names the web host; its path is fetched from this client's API
	// host instead, so credentials never follow a host from a response.
	u, err := url.Parse(out.Data.Attributes.URL)
	if err != nil || u.RawPath != "" || !snapshotView.MatchString(u.Path) {
		return "", errors.New("snapshot response has no valid view URL")
	}
	return u.Path, nil
}

// snapshotView fetches a rendered PNG. Until it is ready, the view answers 200
// with a JSON status document.
func (c *Client) snapshotView(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); media == "image/png" {
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxSnapshotBytes+1))
		if err == nil && len(data) > maxSnapshotBytes {
			err = errors.New("snapshot too large")
		}
		return data, err
	}
	var out struct {
		Data struct {
			Attributes struct {
				Status string `json:"status"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := decodeJSONBody(&out, resp.Body, maxResponseBodyBytes); err != nil {
		return nil, fmt.Errorf("decode snapshot status: %w", err)
	}
	if out.Data.Attributes.Status == "pending" {
		return nil, errSnapshotPending
	}
	return nil, errors.New("snapshot rendering failed")
}

package viz

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// SandboxURL links to the Datadog web sandbox rendering chart, in the format
// the web app builds itself: a gzipped, unpadded base64url definition and the
// chart's millisecond range. The sandbox ignores the definition's own time,
// and without a range shows the last hour.
func SandboxURL(origin string, chart assistant.Chart) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("sandbox origin needs a scheme and host")
	}
	raw, err := json.Marshal(chart.Definition)
	if err != nil {
		return "", err
	}
	var zipped bytes.Buffer
	w := gzip.NewWriter(&zipped)
	if _, err := w.Write(raw); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	query := url.Values{}
	query.Set("widgetDef", base64.RawURLEncoding.EncodeToString(zipped.Bytes()))
	if !chart.End.IsZero() {
		query.Set("start", strconv.FormatInt(chart.Start.UnixMilli(), 10))
		query.Set("end", strconv.FormatInt(chart.End.UnixMilli(), 10))
	}
	u.Path = "/dataviz/sandbox"
	u.RawQuery = query.Encode()
	return u.String(), nil
}

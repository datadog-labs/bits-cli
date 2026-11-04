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

// SandboxURL links to the Datadog web sandbox rendering def, in the format the
// web app builds itself: a gzipped, unpadded base64url definition and the
// widget's millisecond range. The sandbox ignores the definition's own time,
// and without a range shows the last hour.
func SandboxURL(origin string, def assistant.WidgetDefinition) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", errors.New("sandbox origin needs a scheme and host")
	}
	raw, err := json.Marshal(def)
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
	if from, to, ok := fixedRange(def["time"]); ok {
		query.Set("start", strconv.FormatInt(from, 10))
		query.Set("end", strconv.FormatInt(to, 10))
	}
	u.Path = "/dataviz/sandbox"
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// fixedRange reads the fixed millisecond window the backend pins on every
// widget it sends. Restored conversations carry its numbers as floats.
func fixedRange(setting any) (from, to int64, ok bool) {
	t, _ := setting.(map[string]any)
	f, fromOK := t["from"].(float64)
	e, toOK := t["to"].(float64)
	// Within float64's exact integers, so the conversion is exact.
	if !fromOK || !toOK || f < 0 || f >= e || e > 1<<53 {
		return 0, 0, false
	}
	return int64(f), int64(e), true
}

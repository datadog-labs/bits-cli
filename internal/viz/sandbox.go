package viz

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// SandboxURL links to the Datadog web sandbox rendering def, in the format the
// web app builds itself: a gzipped, unpadded base64url definition and the
// widget's millisecond range. The sandbox ignores the definition's own time,
// and without a range shows the last hour, so live spans are resolved here.
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
	if from, to, ok := def.TimeRange(time.Now()); ok {
		query.Set("start", strconv.FormatInt(from, 10))
		query.Set("end", strconv.FormatInt(to, 10))
	}
	u.Path = "/dataviz/sandbox"
	u.RawQuery = query.Encode()
	return u.String(), nil
}

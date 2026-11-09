package viz

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
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
	if from, to, ok := widgetRange(def["time"], time.Now()); ok {
		query.Set("start", strconv.FormatInt(from, 10))
		query.Set("end", strconv.FormatInt(to, 10))
	}
	u.Path = "/dataviz/sandbox"
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// widgetRange resolves a widget's time setting to a millisecond window: a
// fixed window as-is, or a live span ({"type": "live", "value": 1,
// "unit": "year"}) ending at now. Without one, callers use their default.
func widgetRange(setting any, now time.Time) (from, to int64, ok bool) {
	if from, to, ok := fixedRange(setting); ok {
		return from, to, true
	}
	span, ok := liveSpan(setting)
	if !ok {
		return 0, 0, false
	}
	end := now.UnixMilli()
	return end - span.Milliseconds(), end, true
}

// liveSpan reads a live time setting's duration, in the current form or the
// legacy {"live_span": "1w"}. Months and years are 30 and 365 days, as in
// the web app's ONE_MONTH and ONE_YEAR.
func liveSpan(setting any) (time.Duration, bool) {
	t, _ := setting.(map[string]any)
	if legacy, ok := t["live_span"].(string); ok {
		return legacySpan(legacy)
	}
	if t["type"] != "live" {
		return 0, false
	}
	unit, _ := t["unit"].(string)
	value, _ := t["value"].(float64)
	return spanOf(value, unit)
}

// legacySpan parses shorthand such as "15m", "1h", "1mo" or "1y".
func legacySpan(s string) (time.Duration, bool) {
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	value, err := strconv.Atoi(s[:digits])
	unit, known := map[string]string{
		"m": "minute", "h": "hour", "d": "day", "w": "week", "mo": "month", "y": "year",
	}[s[digits:]]
	if err != nil || !known {
		return 0, false
	}
	return spanOf(float64(value), unit)
}

func spanOf(value float64, unit string) (time.Duration, bool) {
	if value <= 0 || value > 1000 || value != float64(int64(value)) {
		return 0, false
	}
	day := 24 * time.Hour
	units := map[string]time.Duration{
		"minute": time.Minute, "hour": time.Hour, "day": day,
		"week": 7 * day, "month": 30 * day, "year": 365 * day,
	}
	per, known := units[unit]
	if !known {
		return 0, false
	}
	if int64(value) > math.MaxInt64/int64(per) {
		return 0, false
	}
	return time.Duration(value) * per, true
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

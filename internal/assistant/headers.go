package assistant

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// headerFieldName is the RFC 9110 token production. It admits no colon,
// whitespace, or newline, so a name can never close a header and open another.
var headerFieldName = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+$`)

// managedHeaders are set by the client itself. They are rejected rather than
// overwritten so a request can never present credentials other than the ones
// the selected authentication mode established, and so an override that would
// be silently discarded is reported instead.
var managedHeaders = map[string]string{
	"authorization":          "the authentication mode sets this",
	"dd-api-key":             "the authentication mode sets this",
	"dd-application-key":     "the authentication mode sets this",
	"content-type":           "the client sets this",
	"x-datadog-bits-surface": "the client sets this",
	// Go sends the host from the request URL and ignores this header, so
	// setting it would be a silent no-op. Honouring it would also let a header
	// retarget the request while it still carries credentials, which the site
	// validation exists to prevent.
	"host": "the Datadog site determines this",
}

// ParseCustomHeaders parses a comma-separated name=value list, for example:
//
//	x-route-hint=1
//	x-route-hint=1,x-trace-me=abc
//
// A value may not contain a comma, since that separates pairs. Whitespace
// around a name or value is ignored, and empty entries are skipped so a
// trailing comma is accepted. It returns nil for an empty list.
//
// Errors describe the offending entry without naming a source, so a caller
// reading the list from somewhere specific should wrap them with it.
func ParseCustomHeaders(raw string) (http.Header, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	headers := http.Header{}
	for _, pair := range strings.Split(raw, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		name, value, found := strings.Cut(pair, "=")
		if !found {
			return nil, fmt.Errorf("entry %q is not name=value", strings.TrimSpace(pair))
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !headerFieldName.MatchString(name) {
			return nil, fmt.Errorf("header name %q is not a valid HTTP header name", name)
		}
		if reason, managed := managedHeaders[strings.ToLower(name)]; managed {
			return nil, fmt.Errorf("must not set %q: %s", name, reason)
		}
		if err := validHeaderValue(value); err != nil {
			return nil, fmt.Errorf("value for %q: %w", name, err)
		}
		headers.Set(name, value)
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return headers, nil
}

// ParseRequiredResponseHeader parses one name=value requirement for a response.
// Unlike custom request headers, any valid field name may be required, including
// names the client manages on outgoing requests.
func ParseRequiredResponseHeader(raw string) (string, string, error) {
	if strings.Contains(raw, ",") {
		return "", "", fmt.Errorf("expected exactly one name=value header")
	}
	name, value, found := strings.Cut(raw, "=")
	if !found {
		return "", "", fmt.Errorf("expected a name=value header")
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if !headerFieldName.MatchString(name) {
		return "", "", fmt.Errorf("header name %q is not a valid HTTP header name", name)
	}
	if value == "" {
		return "", "", fmt.Errorf("required header value must not be empty")
	}
	if err := validHeaderValue(value); err != nil {
		return "", "", fmt.Errorf("value for %q: %w", name, err)
	}
	return http.CanonicalHeaderKey(name), value, nil
}

// deleteManagedHeaders removes every header the client manages. The client
// sets only the ones its authentication mode uses, so clearing the whole set
// is what stops a custom Authorization from surviving an API-key request, or a
// custom DD-API-KEY from surviving an OAuth one.
func deleteManagedHeaders(headers http.Header) {
	for name := range managedHeaders {
		headers.Del(name)
	}
}

// validHeaderValue admits the visible ASCII and horizontal whitespace allowed
// in a field value, so no value can introduce a line break.
func validHeaderValue(value string) error {
	for _, r := range value {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r > 0x7e {
			return fmt.Errorf("contains an unsupported character %q; expected printable ASCII", r)
		}
	}
	return nil
}

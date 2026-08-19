package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Sentinel errors callers can match with errors.Is to branch on the outcome of
// an API call. They are the Unwrap targets of *APIError, so both
//
//	errors.Is(err, assistant.ErrNotFound)
//
// and a type assertion to *APIError (for status/title/detail) work.
var (
	ErrUnauthorized = errors.New("unauthorized") // 401: credentials missing or invalid
	ErrForbidden    = errors.New("forbidden")    // 403: authenticated but not permitted
	ErrNotFound     = errors.New("not found")    // 404: conversation/resource unknown
	ErrConflict     = errors.New("conflict")     // 409: e.g. conversation id already in use
	ErrRateLimited  = errors.New("rate limited") // 429: too many requests
	ErrServer       = errors.New("server error") // 5xx: server-side failure (often retryable)
	ErrBadRequest   = errors.New("bad request")  // 400/422: malformed request
)

// APIError is a structured error from the assistant API. It is decoded from the
// JSON:API error envelope ({"errors":[{status,title,detail,code}]}) that the
// server uses for both regular HTTP error responses (non-streaming routes) and
// in-band errors emitted on the streaming POST (which arrive on a 200 with the
// logical status carried in the envelope's "status" field).
type APIError struct {
	// StatusCode is the HTTP status for non-streaming errors, or the logical
	// status parsed from the envelope for in-band stream errors.
	StatusCode int
	Title      string
	Detail     string
	Code       string
	// Method and Path identify the request, when known.
	Method string
	Path   string
	// Input is the request input that produced the error
	Input any
}

func withInput(err error, input any) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		apiErr.Input = input
	}
	return err
}

func (e *APIError) Error() string {
	msg := e.Detail
	if msg == "" {
		msg = e.Title
	}
	loc := ""
	if e.Method != "" || e.Path != "" {
		loc = strings.TrimSpace(e.Method+" "+e.Path) + ": "
	}
	if msg == "" {
		return fmt.Sprintf("%sstatus %d", loc, e.StatusCode)
	}
	return fmt.Sprintf("%sstatus %d: %s", loc, e.StatusCode, msg)
}

// Unwrap maps the status code onto a sentinel so errors.Is works. Statuses
// without a dedicated sentinel (e.g. 3xx) unwrap to nil.
func (e *APIError) Unwrap() error {
	switch e.StatusCode {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusConflict:
		return ErrConflict
	case http.StatusTooManyRequests:
		return ErrRateLimited
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return ErrBadRequest
	}
	if e.StatusCode >= 500 {
		return ErrServer
	}
	return nil
}

// apiErrorItem is one entry of the JSON:API "errors" array.
type apiErrorItem struct {
	Status string `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

// decodeErrorEnvelope reports whether body is a JSON:API error document and, if
// so, returns its error items. A normal AssistantResponse line has no "errors"
// member, so this cleanly distinguishes in-band errors from ordinary content.
func decodeErrorEnvelope(body []byte) ([]apiErrorItem, bool) {
	var env struct {
		Errors []apiErrorItem `json:"errors"`
	}
	if json.Unmarshal(body, &env) != nil || len(env.Errors) == 0 {
		return nil, false
	}
	return env.Errors, true
}

// newAPIError builds an *APIError from decoded envelope items. httpStatus is the
// fallback status (the real HTTP status for non-streaming errors, or 0 for
// in-band errors where the status rides in the envelope).
func newAPIError(items []apiErrorItem, httpStatus int, method, path string) *APIError {
	e := &APIError{StatusCode: httpStatus, Method: method, Path: path}
	if len(items) > 0 {
		it := items[0]
		e.Title, e.Detail, e.Code = it.Title, it.Detail, it.Code
		if s, err := strconv.Atoi(it.Status); err == nil && s != 0 {
			e.StatusCode = s
		}
	}
	return e
}

// httpError builds an *APIError from an error response body, preferring the
// JSON:API envelope and falling back to a trimmed raw snippet as the detail.
func httpError(body []byte, httpStatus int, method, path string) *APIError {
	if items, ok := decodeErrorEnvelope(body); ok {
		return newAPIError(items, httpStatus, method, path)
	}
	return &APIError{
		StatusCode: httpStatus,
		Detail:     strings.TrimSpace(string(body)),
		Method:     method,
		Path:       path,
	}
}

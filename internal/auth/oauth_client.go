package auth

import (
	"net/http"
	"time"
)

func defaultOAuthClient(c *http.Client, timeout time.Duration) *http.Client {
	if c != nil {
		return c
	}
	return newOAuthHTTPClient(timeout)
}

const (
	loginHTTPTimeout  = 30 * time.Second
	revokeHTTPTimeout = 10 * time.Second
)

// newOAuthHTTPClient builds a client that never follows redirects, so OAuth token and revoke bodies are not replayed to another host.
func newOAuthHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

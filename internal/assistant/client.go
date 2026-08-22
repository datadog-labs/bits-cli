// Package assistant is a bare-bones client for the Datadog Bits AI (CMD-I)
// Assistant HTTP API. It targets the same API the Datadog UI uses when you
// chat with Bits, and is intended to drive a remote agent loop.
//
// The main endpoint (POST /api/v2/assistant) returns a stream of
// newline-delimited JSON, where each line is a complete AssistantResponse.
package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultBaseURL is the Datadog staging site (org 2). dd-auth --domain
// dd.datad0g.com produces credentials valid here.
const DefaultBaseURL = "https://dd.datad0g.com"

const (
	// defaultRequestTimeout bounds each non-streaming request. It matches the
	// server's own non-streaming request timeout (30s).
	defaultRequestTimeout = 30 * time.Second
	// defaultStreamIdleTimeout bounds the gap between streamed lines on the
	// POST. The server pings every 30s while a turn is live, so a turn that
	// goes quiet for longer than this is treated as wedged. It intentionally
	// does NOT cap total turn duration: healthy long turns keep streaming.
	defaultStreamIdleTimeout = 90 * time.Second
	// defaultMaxLineBytes caps a single streamed line so a malformed/endless line
	// can't grow unbounded (the HTTP client sets no body limit). Generous enough
	// for any legitimate line; overridable via Client.MaxLineBytes.
	defaultMaxLineBytes = 128 << 20 // 128 MiB
	// defaultMaxRetries is the number of extra attempts for idempotent,
	// non-streaming requests on transient failures.
	defaultMaxRetries = 2
	// defaultRetryBaseDelay is the base backoff between retries.
	defaultRetryBaseDelay = 200 * time.Millisecond
	// maxRetryDelay caps a single backoff wait.
	maxRetryDelay = 5 * time.Second
)

// AccessTokenSource returns a current OAuth access token. Implementations may
// refresh and persist rotating tokens before returning.
type AccessTokenSource interface {
	AccessToken(context.Context) (string, error)
}

type accessTokenRejector interface {
	RejectAccessToken(context.Context, string) error
}

// Client talks to the Bits AI assistant API over HTTP.
type Client struct {
	BaseURL     string
	APIKey      string
	AppKey      string
	TokenSource AccessTokenSource
	// HTTPClient handles non-streaming requests (history, conversations,
	// skills, flags, rename, share, delete). Its Timeout bounds the whole
	// request. Nil falls back to http.DefaultClient.
	HTTPClient *http.Client
	// StreamClient handles the streaming POST. It must NOT set an overall
	// Timeout, since that would cap total turn duration; idle detection is
	// handled per-read via StreamIdleTimeout. Nil falls back to HTTPClient.
	StreamClient *http.Client
	// StreamIdleTimeout is the maximum gap between streamed lines before the
	// stream is considered wedged. Zero uses defaultStreamIdleTimeout.
	StreamIdleTimeout time.Duration
	// MaxLineBytes caps the size of a single streamed line. Zero uses
	// defaultMaxLineBytes. A larger line fails the stream (wrapping
	// bufio.ErrTooLong) rather than growing unbounded in memory.
	MaxLineBytes int
	// MaxRetries is the number of extra attempts for idempotent, non-streaming
	// requests (GET/DELETE/PUT) on transient failures (network errors,
	// 429/502/503/504). Negative is treated as 0. The streaming POST (Send) is
	// never retried: the API has no idempotency key, so a retried turn would be
	// recorded twice.
	MaxRetries int
	// RetryBaseDelay is the base for exponential backoff between retries. Zero
	// uses defaultRetryBaseDelay.
	RetryBaseDelay time.Duration
	// ForceTracing asks Rapid to manual-keep the streaming Assistant trace.
	// This is intended only for targeted diagnostics because it bypasses normal
	// trace sampling for the request.
	ForceTracing bool
}

// newTransport returns a tuned transport: pooled connections plus bounded
// dial/TLS/response-header phases so a black-holed endpoint fails fast before
// the first byte, without capping a healthy streaming body.
func newTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 100
	tr.MaxIdleConnsPerHost = 10
	tr.IdleConnTimeout = 90 * time.Second
	tr.TLSHandshakeTimeout = 10 * time.Second
	tr.ResponseHeaderTimeout = defaultRequestTimeout
	return tr
}

// NewClient builds a Client from DD_API_KEY / DD_APP_KEY in the environment
// (as populated by dd-auth). This remains the CI and developer fallback while
// interactive users authenticate through NewOAuthClient.
func NewClient() (*Client, error) {
	apiKey := os.Getenv("DD_API_KEY")
	appKey := os.Getenv("DD_APP_KEY")
	if apiKey == "" || appKey == "" {
		return nil, fmt.Errorf("DD_API_KEY and DD_APP_KEY must both be set")
	}
	base := os.Getenv("DD_SITE_URL")
	if base == "" {
		base = DefaultBaseURL
	}
	return NewAPIKeyClient(base, apiKey, appKey)
}

// NewAPIKeyClient builds the explicit developer/CI fallback client.
func NewAPIKeyClient(baseURL, apiKey, appKey string) (*Client, error) {
	if apiKey == "" || appKey == "" {
		return nil, fmt.Errorf("DD_API_KEY and DD_APP_KEY must both be set")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	client := newClient(baseURL)
	client.APIKey = apiKey
	client.AppKey = appKey
	return client, nil
}

// NewOAuthClient builds a Client backed by a refreshing OAuth token source.
func NewOAuthClient(baseURL string, source AccessTokenSource) (*Client, error) {
	if source == nil {
		return nil, fmt.Errorf("OAuth token source is required")
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("OAuth Datadog site is required")
	}
	client := newClient(baseURL)
	client.TokenSource = source
	return client, nil
}

func newClient(baseURL string) *Client {
	tr := newTransport()
	return &Client{
		BaseURL:           strings.TrimRight(baseURL, "/"),
		HTTPClient:        &http.Client{Timeout: defaultRequestTimeout, Transport: tr},
		StreamClient:      &http.Client{Transport: tr}, // no total timeout; idle-bounded
		StreamIdleTimeout: defaultStreamIdleTimeout,
		MaxLineBytes:      defaultMaxLineBytes,
		MaxRetries:        defaultMaxRetries,
		RetryBaseDelay:    defaultRetryBaseDelay,
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) streamClient() *http.Client {
	if c.StreamClient != nil {
		return c.StreamClient
	}
	return c.httpClient()
}

func (c *Client) streamIdleTimeout() time.Duration {
	if c.StreamIdleTimeout > 0 {
		return c.StreamIdleTimeout
	}
	return defaultStreamIdleTimeout
}

func (c *Client) maxLineBytes() int {
	if c.MaxLineBytes > 0 {
		return c.MaxLineBytes
	}
	return defaultMaxLineBytes
}

func (c *Client) maxRetries() int {
	if c.MaxRetries < 0 {
		return 0
	}
	return c.MaxRetries
}

func (c *Client) retryBaseDelay() time.Duration {
	if c.RetryBaseDelay > 0 {
		return c.RetryBaseDelay
	}
	return defaultRetryBaseDelay
}

// newRequest builds an authenticated JSON request. body is marshaled when non-nil.
func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bits-cli/dev")
	req.Header.Set("X-Datadog-Bits-Surface", "cli")
	if c.TokenSource != nil {
		token, err := c.TokenSource.AccessToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("get OAuth access token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("DD-API-KEY", c.APIKey)
		req.Header.Set("DD-APPLICATION-KEY", c.AppKey)
	}
	return req, nil
}

// do issues a non-streaming request and returns the response for the caller to
// decode. On a >=400 status it closes the body and returns a typed *APIError.
//
// do retries transient failures (network errors, 429/502/503/504) up to
// MaxRetries with exponential backoff and jitter, honoring Retry-After. It is
// only used for idempotent requests (GET/DELETE/PUT); the streaming POST does
// not go through do and is never retried.
func (c *Client) rejectAccessToken(ctx context.Context, req *http.Request) {
	rejector, ok := c.TokenSource.(accessTokenRejector)
	if !ok {
		return
	}
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return
	}
	_ = rejector.RejectAccessToken(ctx, token)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	attempts := c.maxRetries() + 1
	var lastErr error
	var wait time.Duration // Retry-After from the previous response, if any
	for attempt := range attempts {
		if attempt > 0 {
			delay := wait
			if delay <= 0 {
				delay = backoffDelay(attempt, c.retryBaseDelay())
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
			wait = 0
		}

		req, err := c.newRequest(ctx, method, path, body)
		if err != nil {
			return nil, err
		}
		resp, err := c.httpClient().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// Network/transport errors are transient; retry until exhausted.
			lastErr = err
			if attempt < attempts-1 {
				continue
			}
			return nil, err
		}
		if resp.StatusCode >= 400 {
			if resp.StatusCode == http.StatusUnauthorized {
				c.rejectAccessToken(ctx, req)
			}
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
			_ = resp.Body.Close()
			apiErr := httpError(snippet, resp.StatusCode, method, path)
			if attempt < attempts-1 && isRetryableStatus(resp.StatusCode) {
				if d, ok := retryAfterDelay(resp.Header); ok {
					wait = d
				}
				lastErr = apiErr
				continue
			}
			return nil, apiErr
		}
		return resp, nil
	}
	return nil, lastErr
}

// isRetryableStatus reports whether a status warrants a retry for an idempotent
// request: rate limiting and transient gateway/service failures.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// backoffDelay returns the wait before the given 1-based retry attempt:
// exponential growth capped at maxRetryDelay, with equal jitter (half fixed,
// half random) to avoid synchronized retries.
func backoffDelay(attempt int, base time.Duration) time.Duration {
	d := base << (attempt - 1)
	if d <= 0 || d > maxRetryDelay { // growth or overflow past the cap
		d = maxRetryDelay
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// retryAfterDelay parses a Retry-After header (delta-seconds or HTTP-date).
func retryAfterDelay(h http.Header) (time.Duration, bool) {
	v := h.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, time.Until(t)), true
	}
	return 0, false
}

// Send posts a user message and invokes fn for every streamed AssistantResponse
// line until the stream ends. Empty server keepalive lines are not surfaced to
// fn. It returns the conversation id observed in the stream (useful when the
// server generated a new one).
//
// Send is never retried on failure: the API has no idempotency key, so
// resending a turn would record the user message and run the agent twice.
// MaxRetries applies only to the idempotent non-streaming calls.
func (c *Client) Send(ctx context.Context, message any, opts SendOptions, fn func(AssistantResponse) error) (string, error) {
	profile := opts.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	attrs := RequestAttributes{
		Message:                   message,
		ConversationID:            opts.ConversationID,
		Model:                     opts.Model,
		Referrer:                  opts.Referrer,
		ClientTools:               opts.ClientTools,
		SkillOverrides:            opts.SkillOverrides,
		Context:                   opts.Context,
		Profile:                   profile,
		CustomUserContext:         opts.CustomUserContext,
		EnableDebugMode:           opts.EnableDebugMode,
		DebugTag:                  opts.DebugTag,
		MessageHistory:            opts.MessageHistory,
		ExperimentalToolOverrides: opts.ExperimentalToolOverrides,
	}
	if opts.StreamToolCallInput {
		attrs.Capabilities = &RequestCapabilities{StreamToolCallInput: true}
	}
	reqBody := Request{Data: RequestData{
		Type:       "assistant-request",
		ID:         "assistant-request",
		Attributes: attrs,
	}}

	path := "/api/v2/assistant"
	if c.ForceTracing {
		path += "?force_tracing=1"
	}
	conversationID := opts.ConversationID

	// Bound the gap between lines (not the total duration) by cancelling a
	// derived context when no data arrives for streamIdleTimeout. The server
	// keepalives every 30s, so an idle window past that means a wedged stream.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var idledOut atomic.Bool
	idle := c.streamIdleTimeout()
	timer := time.AfterFunc(idle, func() {
		idledOut.Store(true)
		cancel()
	})
	defer timer.Stop()

	req, err := c.newRequest(streamCtx, http.MethodPost, path, reqBody)
	if err != nil {
		return conversationID, err
	}
	resp, err := c.streamClient().Do(req)
	if err != nil {
		return conversationID, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		if resp.StatusCode == http.StatusUnauthorized {
			c.rejectAccessToken(ctx, req)
		}
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return conversationID, httpError(snippet, resp.StatusCode, http.MethodPost, path)
	}

	// The stream is newline-delimited JSON (records separated by "\n\n"). A
	// bounded Scanner caps per-line memory: the HTTP client won't limit the
	// body, so an endless line without a newline could otherwise grow
	// unbounded. The cap is generous and configurable via MaxLineBytes.
	maxLine := c.maxLineBytes()
	initial := min(64*1024, maxLine)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, initial), maxLine)
	for scanner.Scan() {
		trimmed := bytes.TrimSpace(scanner.Bytes())
		timer.Reset(idle)
		if len(trimmed) == 0 {
			continue
		}
		var ar AssistantResponse
		if err := json.Unmarshal(trimmed, &ar); err != nil {
			return conversationID, fmt.Errorf("decode stream line: %w: %s", err, trimmed)
		}
		// In-band errors arrive on a 200 as a JSON:API error document
		// ({"errors":[...]}) with no data; a normal line has no "errors".
		if len(ar.Errors) > 0 {
			return conversationID, newAPIError(ar.Errors, 0, http.MethodPost, path)
		}
		if id := ar.Data.Attributes.ConversationID; id != "" {
			conversationID = id
		}
		// The server emits empty keepalive lines (~every 30s) to hold the
		// connection open while the agent works. They reset the idle timer
		// above and can carry the conversation id, but have no message, so
		// don't surface them to the caller.
		if fn != nil && !isKeepalive(ar) {
			if err := fn(ar); err != nil {
				return conversationID, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return conversationID, fmt.Errorf("stream line exceeds max size (%d bytes): %w", maxLine, err)
		}
		if idledOut.Load() {
			return conversationID, &APIError{
				StatusCode: http.StatusGatewayTimeout,
				Title:      "Stream idle timeout",
				Detail:     fmt.Sprintf("no data received for %s", idle),
				Method:     http.MethodPost,
				Path:       path,
			}
		}
		if e := ctx.Err(); e != nil {
			return conversationID, e
		}
		return conversationID, fmt.Errorf("read stream: %w", err)
	}
	return conversationID, nil
}

// isKeepalive reports whether ar is a server keepalive: no message and no debug
// prompt. The server emits these while a turn is in flight to hold the
// connection open (structured_message is null). A real message always carries a
// message_id, and a debug line carries a prompt, so both are distinguishable
// without re-parsing the raw line.
func isKeepalive(ar AssistantResponse) bool {
	return ar.Data.Attributes.StructuredMessage.MessageID == "" &&
		ar.Data.Attributes.Prompt == ""
}

// ToolExecutor runs a client tool given its arguments as the raw JSON string
// the model produced (metadata.input), and returns the tool output that is
// echoed back to the model. A non-nil error is reported to the model as a
// failed tool result rather than aborting the loop.
type ToolExecutor func(ctx context.Context, input string) (output string, err error)

// Tool bundles a client-side tool definition with the function that executes
// it locally.
type Tool struct {
	ClientTool
	Run ToolExecutor
}

// ConversationHistory fetches the full message history for a conversation.
func (c *Client) ConversationHistory(ctx context.Context, in ConversationHistoryInput) (*ConversationHistoryResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/conversation-history/"+in.ConversationID, nil)
	if err != nil {
		return nil, withInput(err, in)
	}
	defer func() { _ = resp.Body.Close() }()
	var out ConversationHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode history: %w", err)
	}
	return &out, nil
}

// UserConversations lists all conversations for the current user.
func (c *Client) UserConversations(ctx context.Context) (*UserConversationsResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/user-conversations", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out UserConversationsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode conversations: %w", err)
	}
	return &out, nil
}

// ListSkills fetches the skills available to the caller's org from
// GET /api/v2/assistant/skills. Enable one for a turn via
// SendOptions.SkillOverrides.
func (c *Client) ListSkills(ctx context.Context) ([]Skill, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/skills", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out SkillsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode skills: %w", err)
	}
	return out.Data.Attributes.Skills, nil
}

// ExperimentalToolFlags reports which feature flags are active for the org.
func (c *Client) ExperimentalToolFlags(ctx context.Context) (map[string]bool, error) {
	resp, err := c.do(ctx, http.MethodGet, "/api/v2/assistant/experimental-tool-flags", nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var out ExperimentalToolFlagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode flags: %w", err)
	}
	return out.Data.Attributes.Flags, nil
}

// DeleteConversation deletes a conversation by id.
func (c *Client) DeleteConversation(ctx context.Context, in DeleteConversationInput) error {
	resp, err := c.do(ctx, http.MethodDelete, "/api/v2/assistant/conversation/"+in.ConversationID, nil)
	if err != nil {
		return withInput(err, in)
	}
	_ = resp.Body.Close()
	return nil
}

// RenameConversation sets a conversation's title (1-200 chars) via
// PUT /api/v2/assistant/user-conversations/{id}/title. The server returns no
// body on success.
func (c *Client) RenameConversation(ctx context.Context, in RenameConversationInput) error {
	reqBody := updateConversationRequest{Data: updateConversationData{
		Type:       "update-conversation-request",
		Attributes: updateConversationAttributes{Title: in.Title},
	}}
	resp, err := c.do(ctx, http.MethodPut, "/api/v2/assistant/user-conversations/"+in.ConversationID+"/title", reqBody)
	if err != nil {
		return withInput(err, in)
	}
	_ = resp.Body.Close()
	return nil
}

// ShareConversation toggles sharing for a conversation via
// PUT /api/v2/assistant/conversation/{id}/is_shared. When in.Shared is true,
// in.TTLDays optionally sets the expiry window (1-1095 days); pass nil for the
// server default. When in.Shared is false, in.TTLDays is ignored. It returns
// the updated conversation summary.
func (c *Client) ShareConversation(ctx context.Context, in ShareConversationInput) (*ConversationSummary, error) {
	reqBody := updateSharingRequest{IsShared: in.Shared}
	if in.Shared {
		reqBody.TTLDays = in.TTLDays
	}
	resp, err := c.do(ctx, http.MethodPut, "/api/v2/assistant/conversation/"+in.ConversationID+"/is_shared", reqBody)
	if err != nil {
		return nil, withInput(err, in)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Data struct {
			Attributes ConversationSummary `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode sharing response: %w", err)
	}
	return &out.Data.Attributes, nil
}

// Package datadogmcp connects to Datadog MCP using existing Bits credentials.
package datadogmcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/site"
	"github.com/datadog-labs/bits-cli/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpProtocolVersion = "2025-03-26"
	toolRequestTimeout = time.Minute

	toolGetWidget    = "get_widget"
	toolGetWidgetV2  = "get_widget_v2"
	toolFetchData    = "_dd_wfd"
	toolFeatureFlags = "_dd_gff"
	toolUserConfig   = "_dd_guc"
)

type Client struct {
	endpoint       string
	token          assistant.AccessTokenSource
	apiKey, appKey string
	http           *http.Client
	gate           chan struct{} // guards session state and serializes initialization
	session        *mcp.ClientSession
	allowed        map[string]bool
	closed         bool
}

// New borrows the existing credential source. The caller owns Close; connection
// and read-only tool discovery happen lazily on the first call.
func New(client *assistant.Client) (*Client, error) {
	if client == nil {
		return nil, errors.New("visualizations require a Datadog API client")
	}
	u, err := url.Parse(client.BaseURL)
	if err != nil {
		return nil, err
	}
	host := strings.ToLower(u.Hostname())
	domain := strings.TrimPrefix(host, "api.")
	if u.Scheme != "https" || u.User != nil || u.Host != host || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || domain == host {
		return nil, errors.New("visualizations require a Datadog API site")
	}
	// The site catalog includes exact production and configured staging hosts.
	if _, trusted := site.WebHostForAssistantHost(host); !trusted {
		return nil, errors.New("visualizations require a supported Datadog API site")
	}
	return &Client{
		gate:     make(chan struct{}, 1),
		endpoint: "https://mcp." + domain + "/v1/mcp?toolsets=core,visualizations,widgets",
		token:    client.TokenSource, apiKey: client.APIKey, appKey: client.AppKey,
		http: &http.Client{Timeout: toolRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func supportedTool(name string) bool {
	switch name {
	case toolFetchData, toolFeatureFlags, toolUserConfig, toolGetWidget, toolGetWidgetV2:
		return true
	}
	return false
}

func (c *Client) sessionFor(ctx context.Context, name string) (*mcp.ClientSession, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed {
		return nil, errors.New("MCP client closed")
	}
	if c.session != nil {
		return c.authorizedSession(name)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "bits-cli", Version: version.Build()}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	httpClient := *c.http
	httpClient.Transport = &mcpAuthTransport{next: c.http.Transport, token: c.token, apiKey: c.apiKey, appKey: c.appKey}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: c.endpoint, HTTPClient: &httpClient, DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: mcpProtocolVersion})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool)
	for tool, listErr := range session.Tools(ctx, nil) {
		if listErr != nil {
			_ = session.Close()
			return nil, listErr
		}
		if supportedTool(tool.Name) && tool.Annotations != nil && tool.Annotations.ReadOnlyHint {
			allowed[tool.Name] = true
		}
	}
	c.session = session
	c.allowed = allowed
	go func() {
		_ = session.Wait()
		c.retire(session)
	}()
	return c.authorizedSession(name)
}

// authorizedSession is called with the gate held.
func (c *Client) authorizedSession(name string) (*mcp.ClientSession, error) {
	if !c.allowed[name] {
		return nil, fmt.Errorf("datadog has not exposed %s as a read-only tool", name)
	}
	return c.session, nil
}

func (c *Client) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	if params == nil {
		return nil, errors.New("missing MCP call parameters")
	}
	name := params.Name
	if !supportedTool(name) {
		return nil, fmt.Errorf("visualization tool %q is not supported", name)
	}
	session, err := c.sessionFor(ctx, name)
	if err != nil {
		return nil, err
	}
	result, err := session.CallTool(ctx, params)
	if err != nil && ctx.Err() == nil && terminalCallError(err) {
		c.retire(session)
	}
	return result, err
}

// The SDK closes the connection for these failures, but its Wait goroutine may
// not have cleared our cache by the time CallTool returns. The two text checks
// cover SDK errors that do not expose a sentinel; transient errors stay cached.
func terminalCallError(err error) bool {
	if errors.Is(err, mcp.ErrConnectionClosed) || errors.Is(err, mcp.ErrSessionMissing) {
		return true
	}
	message := err.Error()
	return strings.Contains(message, "sending \"tools/call\": Unauthorized") ||
		strings.Contains(message, "failed to decode response:")
}

// A late termination must not evict a replacement session or its permissions.
func (c *Client) retire(session *mcp.ClientSession) {
	c.gate <- struct{}{}
	if c.session == session {
		c.session = nil
		c.allowed = nil
	}
	<-c.gate
	_ = session.Close()
}

func (c *Client) Close() error {
	c.gate <- struct{}{}
	c.closed = true
	session := c.session
	c.session = nil
	c.allowed = nil
	<-c.gate
	if session != nil {
		return session.Close()
	}
	return nil
}

// mcpAuthTransport adds Bits credentials to each SDK request. The endpoint is
// validated before constructing the SDK transport, and the HTTP client refuses redirects.
type mcpAuthTransport struct {
	next           http.RoundTripper
	token          assistant.AccessTokenSource
	apiKey, appKey string
}

func (t *mcpAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", "bits-cli/"+version.Build())
	req.Header.Set("X-Datadog-MCP-Referrer-Name", "bits-cli")
	req.Header.Set("X-Datadog-MCP-Referrer-Version", version.Build())
	if t.token != nil {
		token, err := t.token.AccessToken(req.Context())
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Set("DD-API-KEY", t.apiKey)
		req.Header.Set("DD-APPLICATION-KEY", t.appKey)
	}
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		if rejector, ok := t.token.(interface {
			RejectAccessToken(context.Context, string) error
		}); ok {
			_ = rejector.RejectAccessToken(req.Context(), strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		}
	}
	// SDK errors may include a JSON-RPC response message. Do not expose an
	// unauthenticated server's body to the terminal or native viewer.
	if resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		resp.Body = http.NoBody
	} else if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		// MaxEventSize caps SSE events, but the SDK's JSON path uses io.ReadAll.
		resp.Body = http.MaxBytesReader(nil, resp.Body, mcp.DefaultMaxEventSize)
	}
	return resp, nil
}

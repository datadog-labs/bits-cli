package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/datadog-labs/bits-cli/internal/auth"
	"github.com/datadog-labs/bits-cli/internal/datadogmcp"
	"github.com/datadog-labs/bits-cli/internal/version"
	"github.com/datadog-labs/bits-cli/internal/viz"
)

// chatWidgets is the chat's widget client: wiring, not a command. It
// authenticates on the first operation, on an operation worker rather than the
// TUI event loop, with the chat's own credentials. It then renders with ddviz
// through Datadog MCP, or with Datadog snapshots where ddviz cannot run, and
// owns that client and any MCP connection.
type chatWidgets struct {
	mu     sync.Mutex
	opts   ChatOptions
	store  auth.CredentialStore
	local  bool // ddviz runs here; otherwise Datadog renders snapshots
	client viz.Client
	mcp    *datadogmcp.Client // nil for snapshots
	closed bool
}

// EnvViz selects the widget renderer: auto (the default) prefers ddviz, which
// also opens the interactive panel; snapshot always uses Datadog snapshots;
// off shows widgets as links.
const EnvViz = "BITS_VIZ"

func newChatWidgets(opts ChatOptions, store auth.CredentialStore) (viz.Client, error) {
	renderer, err := vizRenderer(os.Getenv(EnvViz))
	if err != nil || renderer == "off" {
		return nil, err
	}
	return &chatWidgets{opts: opts, store: store, local: renderer == "auto" && viz.Supported()}, nil
}

func vizRenderer(value string) (string, error) {
	switch renderer := strings.ToLower(strings.TrimSpace(value)); renderer {
	case "", "auto":
		return "auto", nil
	case "snapshot", "off":
		return renderer, nil
	default:
		return "", fmt.Errorf("%s: unknown renderer %q; use auto, snapshot, or off", EnvViz, value)
	}
}

// errUnsupportedSite marks a site that serves no visualizations: a reason to
// fall back to links, not a failure to report.
var errUnsupportedSite = errors.New("site does not serve visualizations")

func (c *chatWidgets) get(ctx context.Context) (viz.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, context.Canceled
	}
	if c.client != nil {
		return c.client, nil
	}
	api, err := NewAuthenticatedClient(ctx, clientOptions(c.opts, c.store))
	if err != nil {
		return nil, err
	}
	if !c.local {
		c.client = viz.NewRemote(api)
		return c.client, nil
	}
	mcp, err := datadogmcp.New(api)
	if err != nil {
		return nil, errors.Join(errUnsupportedSite, err)
	}
	c.mcp = mcp
	c.client = viz.New(viz.Config{MCP: mcp, FrameURL: api.BaseURL + "/api/unstable/dataviz-mcp-ui", Version: version.Build()})
	return c.client, nil
}

func widgetsFailure(err error) viz.Status {
	switch {
	case errors.Is(err, context.Canceled):
		return viz.Status{Phase: viz.Canceled}
	case errors.Is(err, errUnsupportedSite):
		return viz.Status{Phase: viz.Unavailable, Reason: "Visualizations are not available on this site."}
	}
	return viz.Status{Phase: viz.Failed, Reason: "Visualization access could not be initialized.", Retry: viz.RetryAllowed}
}

// Render and Open rely on viz to bound each operation once connected.
func (c *chatWidgets) Render(ctx context.Context, r viz.Request) viz.RenderResult {
	client, err := c.get(ctx)
	if err != nil {
		return viz.RenderResult{Status: widgetsFailure(err)}
	}
	return client.Render(ctx, r)
}

func (c *chatWidgets) Open(ctx context.Context, r viz.Request) viz.Status {
	client, err := c.get(ctx)
	if err != nil {
		return widgetsFailure(err)
	}
	return client.Open(ctx, r)
}

func (c *chatWidgets) Interactive() bool { return c.local }

func (c *chatWidgets) Close() error {
	c.mu.Lock()
	c.closed = true
	client, mcp := c.client, c.mcp
	c.mu.Unlock()
	if client == nil {
		return nil
	}
	if mcp == nil {
		return client.Close()
	}
	return errors.Join(client.Close(), mcp.Close())
}

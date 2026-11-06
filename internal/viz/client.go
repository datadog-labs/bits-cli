package viz

import (
	"context"
	"errors"
	"net/url"
	"os"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/datadog-labs/bits-cli/internal/libexec"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config binds a client to one account/site. Recreate it on account changes.
// MCP owns authentication and authorization of read-only MCP tools.
type Config struct {
	MCP      MCPClient
	FrameURL string
	Version  string
	Debug    bool
	DataDir  string

	// RenderConcurrency limits hidden viewers. Values <= 0 use two.
	// Viewers start lazily; the interactive panel has its own single viewer.
	RenderConcurrency int
}

type localClient struct {
	ctx         context.Context
	cancel      context.CancelFunc
	launch      launchFunc
	mcp         MCPClient
	renderers   chan viewer
	interactive chan viewer
	closeOnce   sync.Once
}

// Supported reports whether this system can run the ddviz helper. Without it,
// widgets fall back to Datadog snapshots (NewRemote).
func Supported() bool {
	// ddviz is built for macOS only; an exec failure covers the rest.
	if runtime.GOOS != "darwin" {
		return false
	}
	binary, err := libexec.Binary("ddviz")
	if err != nil {
		return false
	}
	_, err = os.Stat(binary)
	return err == nil
}

// New creates a lazy local ddviz client. Unsupported systems and missing helpers
// are reported through operation outcomes. No process starts here.
func New(config Config) Client {
	c := newLocal(nil, config.RenderConcurrency)
	c.mcp = config.MCP
	c.launch = func(lifetime, startup context.Context, headless bool) (viewer, error) {
		u, err := url.Parse(config.FrameURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || config.MCP == nil {
			return nil, errors.New("visualizations require authenticated tool access and an HTTPS frame URL")
		}
		binary, err := libexec.Binary("ddviz")
		if err != nil {
			return nil, err
		}
		return launch(lifetime, startup, binary, config, headless)
	}
	return c
}

func newLocal(start launchFunc, concurrency int) *localClient {
	if concurrency <= 0 {
		concurrency = 2
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &localClient{
		ctx:         ctx,
		cancel:      cancel,
		launch:      start,
		renderers:   make(chan viewer, concurrency),
		interactive: make(chan viewer, 1),
	}
	// A nil viewer represents an available slot that has not started a process.
	for range concurrency {
		client.renderers <- nil
	}
	client.interactive <- nil
	return client
}

// transientStart are exec errors from a system short of resources.
var transientStart = []error{syscall.EAGAIN, syscall.ENOMEM, syscall.ETXTBSY, syscall.EMFILE, syscall.ENFILE}

func failure(err error) Status {
	if errors.Is(err, context.Canceled) {
		return Status{Phase: Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Status{Phase: Failed, Reason: "Visualization timed out.", Retry: RetryAllowed}
	}
	var start *startError
	if errors.As(err, &start) {
		switch {
		case errors.Is(start.err, os.ErrNotExist) || errors.Is(start.err, syscall.ENOEXEC):
			// Missing, or not executable on this system.
			return Status{Phase: Unavailable, Reason: "The visualization helper cannot run on this system."}
		case slices.ContainsFunc(transientStart, func(e error) bool { return errors.Is(start.err, e) }):
			return Status{Phase: Failed, Reason: "The visualization helper could not start.", Retry: RetryAllowed}
		}
		return Status{Phase: Failed, Reason: "The visualization helper could not start. Check its installation.", Retry: RetryNotUseful}
	}
	return Status{Phase: Failed, Reason: "Visualization could not be loaded.", Retry: RetryAllowed}
}

func (c *localClient) perform(ctx context.Context, request Request, headless bool) (*Snapshot, Status) {
	if c.ctx.Err() != nil {
		return nil, Status{Phase: Canceled}
	}
	if !request.valid() {
		return nil, Status{Phase: Failed, Reason: "Request is invalid.", Retry: RetryNotUseful}
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()

	pool := c.renderers
	if !headless {
		pool = c.interactive
	}

	var current viewer
	select {
	case current = <-pool:
	case <-ctx.Done():
		return nil, failure(ctx.Err())
	}
	defer func() { pool <- current }()

	if err := ctx.Err(); err != nil {
		return nil, failure(err)
	}

	if current != nil && current.exited() {
		current.close()
		current = nil
	}
	if current == nil {
		started, err := c.launch(c.ctx, ctx, headless)
		if err != nil {
			return nil, failure(err)
		}
		current = started
	}

	// Closing an active viewer on cancellation prevents late chart callbacks from
	// affecting the next request, and interrupts blocked pipe writes.
	finished := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		current.close()
		close(finished)
	})
	result, status := c.present(ctx, current, request, headless)
	if !stopClose() {
		<-finished
		current = nil
	} else if status.Phase != Succeeded {
		current.close()
		current = nil
	}

	if err := ctx.Err(); err != nil {
		return nil, failure(err)
	}

	return result, status
}

func (c *localClient) fetchWidget(ctx context.Context, request Request) (*mcp.CallToolResult, Status) {
	result, err := c.mcp.CallTool(ctx, &mcp.CallToolParams{
		Name: "get_widget",
		Arguments: map[string]any{
			"widget_definition": request.Definition,
		},
	})
	if err != nil {
		return nil, failure(err)
	}
	if result == nil || result.IsError || result.StructuredContent == nil {
		return nil, Status{Phase: Failed, Reason: "Datadog did not return a renderable widget.", Retry: RetryNotUseful}
	}
	return result, Status{Phase: Succeeded}
}

func (c *localClient) present(ctx context.Context, viewer viewer, request Request, headless bool) (*Snapshot, Status) {
	result, status := c.fetchWidget(ctx, request)
	if status.Phase != Succeeded {
		return nil, status
	}
	if err := viewer.present(result); err != nil {
		return nil, failure(err)
	}
	if !headless {
		if err := viewer.show(); err != nil {
			return nil, failure(err)
		}
		return nil, Status{Phase: Succeeded}
	}
	data, err := viewer.snapshot(ctx)
	if err != nil {
		return nil, failure(err)
	}
	image, err := NewSnapshot(data, Availability{Available: true})
	if err != nil {
		return nil, failure(err)
	}
	return image, Status{Phase: Succeeded}
}

func (c *localClient) Render(ctx context.Context, request Request) RenderResult {
	image, status := c.perform(ctx, request, true)
	return RenderResult{Status: status, Image: image}
}

func (c *localClient) Open(ctx context.Context, request Request) Status {
	_, status := c.perform(ctx, request, false)
	return status
}

func (c *localClient) Interactive() bool { return true }

func (c *localClient) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		// Drain every slot, including active operations as they return their viewers.
		// Never close the channels: queued callers leave through context cancellation.
		for _, pool := range []chan viewer{c.renderers, c.interactive} {
			for range cap(pool) {
				if current := <-pool; current != nil {
					current.close()
				}
			}
		}
	})
	return nil
}

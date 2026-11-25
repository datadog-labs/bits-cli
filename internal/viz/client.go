package viz

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/libexec"
)

// API reopens saved apps and relays their tool calls; *assistant.Client
// implements it. The server decides which tools an app may call.
type API interface {
	RestoreApp(ctx context.Context, conversationID, messageID string) (assistant.AppPayload, error)
	CallAppTool(ctx context.Context, relayHandle, name string, arguments json.RawMessage) (json.RawMessage, error)
}

// Config binds a client to one account/site. Recreate it on account changes.
type Config struct {
	API API
	// CallLegacyTool serves the direct, read-only MCP route for legacy charts.
	CallLegacyTool func(context.Context, string, json.RawMessage) (json.RawMessage, error)
	FrameURL       string
	Version        string
	Debug          bool
	DataDir        string

	// RenderConcurrency limits hidden viewers. Values <= 0 use two.
	// Viewers start lazily; the interactive panel has its own single viewer.
	RenderConcurrency int
}

type localClient struct {
	ctx            context.Context
	cancel         context.CancelFunc
	launch         launchFunc
	api            API
	callLegacyTool toolCaller
	renderers      chan viewerSession
	interactive    chan viewerSession
	closeOnce      sync.Once
	// expired holds relay handles the server reported gone, including those
	// detected by callbacks after Open has returned.
	expired sync.Map
}

// A frame can keep calling tools after presentation, so its relay session
// cannot change while the viewer is alive.
type viewerSession struct {
	viewer
	relayHandle string // empty for the shared legacy MCP connection
}

// Supported reports whether this system can run the ddviz helper. Without it,
// charts fall back to Datadog snapshots (NewRemote).
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
	c.api = config.API
	c.callLegacyTool = config.CallLegacyTool
	c.launch = func(lifetime, startup context.Context, headless bool, tools toolCaller) (viewer, error) {
		u, err := url.Parse(config.FrameURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || config.API == nil {
			return nil, errors.New("visualizations require authenticated tool access and an HTTPS frame URL")
		}
		binary, err := libexec.Binary("ddviz")
		if err != nil {
			return nil, err
		}
		return launch(lifetime, startup, binary, config, headless, tools)
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
		renderers:   make(chan viewerSession, concurrency),
		interactive: make(chan viewerSession, 1),
	}
	// An empty session represents an available slot with no process.
	for range concurrency {
		client.renderers <- viewerSession{}
	}
	client.interactive <- viewerSession{}
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

func (c *localClient) perform(ctx context.Context, request Request, headless bool) Result {
	if c.ctx.Err() != nil {
		return Result{Status: Status{Phase: Canceled}}
	}

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()

	var app assistant.AppPayload
	var updated *assistant.AppPayload
	tools := c.callLegacyTool
	if request.LegacyWidget == nil {
		// Restore before taking a viewer, which the restore would leave idle.
		var status Status
		app, updated, status = c.loadForViewer(ctx, request)
		if status.Phase != Succeeded {
			return Result{Status: status, App: updated}
		}
		tools = c.relay(app.RelayHandle)
	}

	pool := c.renderers
	if !headless {
		pool = c.interactive
	}

	var current viewerSession
	select {
	case current = <-pool:
	case <-ctx.Done():
		return Result{Status: failure(ctx.Err()), App: updated}
	}
	defer func() { pool <- current }()

	if err := ctx.Err(); err != nil {
		return Result{Status: failure(err), App: updated}
	}

	if request.LegacyWidget != nil {
		// Keep direct fetches under the same concurrency limit as their previews.
		var status Status
		app, status = c.fetchLegacyWidget(ctx, request.LegacyWidget)
		if status.Phase != Succeeded {
			return Result{Status: status}
		}
	}

	if current.viewer != nil && (current.relayHandle != app.RelayHandle || current.exited()) {
		current.close()
		current = viewerSession{}
	}
	if current.viewer == nil {
		started, err := c.launch(c.ctx, ctx, headless, tools)
		if err != nil {
			return Result{Status: failure(err), App: updated}
		}
		current = viewerSession{viewer: started, relayHandle: app.RelayHandle}
	}

	// Closing an active viewer on cancellation prevents late chart callbacks from
	// affecting the next request, and interrupts blocked pipe writes.
	finished := make(chan struct{})
	stopClose := context.AfterFunc(ctx, func() {
		current.close()
		close(finished)
	})
	image, status := present(ctx, current, app, headless)
	// A frame can produce a valid PNG of its error state after the relay expires.
	if _, gone := c.expired.Load(app.RelayHandle); gone {
		image, status = nil, expiredRelay
		if request.CanRestore() {
			status.Retry = RetryAllowed
		} else {
			app.RelayHandle = ""
			updated = &app
		}
	}
	if !stopClose() {
		<-finished
		current = viewerSession{}
	} else if status.Phase != Succeeded {
		current.close()
		current = viewerSession{}
	}

	if err := ctx.Err(); err != nil {
		return Result{Status: failure(err), App: updated}
	}
	return Result{Status: status, Image: image, App: updated}
}

var expiredRelay = Status{Phase: Failed, Reason: "The visualization session expired."}

func (c *localClient) loadForViewer(ctx context.Context, request Request) (assistant.AppPayload, *assistant.AppPayload, Status) {
	// An app without a live relay session fails its tool calls: restore it for
	// a new one.
	_, gone := c.expired.Load(request.App.RelayHandle)
	if gone || request.App.RelayHandle == "" {
		if !request.CanRestore() {
			if gone {
				request.App.RelayHandle = ""
				return assistant.AppPayload{}, &request.App, expiredRelay
			}
			return assistant.AppPayload{}, nil, notShown
		}
		request.App.Result, request.App.RelayHandle = nil, ""
	}
	app, restored, status := load(ctx, c.api, request)
	if status.Phase == Succeeded && app.RelayHandle == "" {
		return assistant.AppPayload{}, restored, notShown
	}
	return app, restored, status
}

// relay sends an app's tool calls through its relay session.
func (c *localClient) relay(handle string) toolCaller {
	return func(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
		if handle == "" {
			return nil, errors.New("the app has no relay session")
		}
		result, err := c.api.CallAppTool(ctx, handle, name, arguments)
		if errors.Is(err, assistant.ErrGone) {
			c.expired.Store(handle, struct{}{})
		}
		return result, err
	}
}

func present(ctx context.Context, viewer viewer, app assistant.AppPayload, headless bool) (*Snapshot, Status) {
	if err := viewer.present(app); err != nil {
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

func (c *localClient) Render(ctx context.Context, request Request) Result {
	return c.perform(ctx, request, true)
}

func (c *localClient) Open(ctx context.Context, request Request) Result {
	return c.perform(ctx, request, false)
}

func (c *localClient) Interactive() bool { return true }

func (c *localClient) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		// Drain every slot, including active operations as they return their viewers.
		// Never close the channels: queued callers leave through context cancellation.
		for _, pool := range []chan viewerSession{c.renderers, c.interactive} {
			for range cap(pool) {
				if current := <-pool; current.viewer != nil {
					current.close()
				}
			}
		}
	})
	return nil
}

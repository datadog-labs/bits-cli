package viz

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// SnapshotAPI renders a widget server side, and reopens saved apps;
// *assistant.Client implements it.
type SnapshotAPI interface {
	RenderSnapshot(context.Context, assistant.SnapshotOptions) ([]byte, error)
	RestoreApp(ctx context.Context, conversationID, messageID string) (assistant.AppPayload, error)
}

// remoteClient previews charts with Datadog's snapshot service, for systems
// where ddviz cannot run. It has no panel, and other views have no preview.
type remoteClient struct {
	api    SnapshotAPI
	ctx    context.Context // canceled by Close
	cancel context.CancelFunc
	slots  chan struct{}
	work   sync.WaitGroup
	mu     sync.Mutex // orders work.Add against Close's Wait
}

// NewRemote borrows api, which must stay usable until Close returns.
func NewRemote(api SnapshotAPI) Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &remoteClient{api: api, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 4)}
}

// begin registers work that Close cancels and waits for. It reports false
// once the client is closed.
func (c *remoteClient) begin(ctx context.Context) (context.Context, func(), bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx.Err() != nil {
		return nil, nil, false
	}
	c.work.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() { stop(); cancel(); c.work.Done() }, true
}

func (c *remoteClient) Render(ctx context.Context, r Request) Result {
	ctx, done, ok := c.begin(ctx)
	if !ok {
		return Result{Status: Status{Phase: Canceled}}
	}
	defer done()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return Result{Status: failure(ctx.Err())}
	}
	// The budget starts with the slot, so queued previews do not time out.
	ctx, cancelTimeout := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelTimeout()

	app, restored, status := load(ctx, c.api, r)
	if status.Phase != Succeeded {
		return Result{Status: status}
	}
	chart := app.Summary().Chart
	if chart == nil {
		// Unavailable would mean snapshots cannot run here at all.
		return Result{Status: notShown, App: restored}
	}
	start, end := chart.Start, chart.End
	if end.IsZero() {
		end = time.Now()
		start = end.Add(-time.Hour)
	}
	data, err := c.api.RenderSnapshot(ctx, assistant.SnapshotOptions{
		Definition: chart.Definition, Start: start, End: end,
		Width: 820, Height: 450, Dark: r.Dark, // ddviz's usual size
	})
	if err != nil {
		return Result{Status: remoteFailure(err), App: restored}
	}
	image, err := NewSnapshot(data, Availability{})
	if err != nil {
		return Result{Status: Status{Phase: Failed, Reason: "Datadog returned an unreadable image.", Retry: RetryAllowed}, App: restored}
	}
	return Result{Status: Status{Phase: Succeeded}, Image: image, App: restored}
}

func remoteFailure(err error) Status {
	var api *assistant.APIError
	switch {
	case errors.Is(err, assistant.ErrBadRequest) && errors.As(err, &api) && api.Detail != "":
		return Status{Phase: Failed, Reason: api.Detail, Retry: RetryNotUseful}
	case errors.Is(err, assistant.ErrForbidden) || errors.Is(err, assistant.ErrNotFound):
		// Only creation is typed: this login or site cannot create snapshots.
		return Status{Phase: Unavailable}
	}
	return failure(err)
}

// notShown is a view snapshots cannot preview and the web sandbox cannot open:
// only charts have either.
var notShown = Status{Phase: Failed, Reason: "This view can't be shown here.", Retry: RetryNotUseful}

// Open has no panel to present. It restores a saved app, so the caller can
// fall back to the web with its chart, and reports Unavailable.
func (c *remoteClient) Open(ctx context.Context, r Request) Result {
	ctx, done, ok := c.begin(ctx)
	if !ok {
		return Result{Status: Status{Phase: Canceled}}
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	app, restored, status := load(ctx, c.api, r)
	if status.Phase != Succeeded {
		return Result{Status: status}
	}
	if app.Summary().Chart == nil {
		return Result{Status: notShown, App: restored}
	}
	return Result{Status: Status{Phase: Unavailable}, App: restored}
}

func (c *remoteClient) Interactive() bool { return false }

func (c *remoteClient) Close() error {
	c.mu.Lock()
	c.cancel()
	c.mu.Unlock()
	c.work.Wait()
	return nil
}

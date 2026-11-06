package viz

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// SnapshotAPI renders a widget server side; *assistant.Client implements it.
type SnapshotAPI interface {
	RenderSnapshot(context.Context, assistant.SnapshotOptions) ([]byte, error)
}

// remoteClient previews widgets with Datadog's snapshot service, for systems
// where ddviz cannot run. It has no panel.
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

func (c *remoteClient) Render(ctx context.Context, r Request) RenderResult {
	if !r.valid() {
		return RenderResult{Status: Status{Phase: Failed, Reason: "Request is invalid.", Retry: RetryNotUseful}}
	}
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return RenderResult{Status: Status{Phase: Canceled}}
	}
	c.work.Add(1)
	c.mu.Unlock()
	defer c.work.Done()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return RenderResult{Status: failure(ctx.Err())}
	}
	// The budget starts with the slot, so queued previews do not time out.
	ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
	defer cancelTimeout()

	end := time.Now()
	start := end.Add(-time.Hour)
	if from, to, ok := fixedRange(r.Definition["time"]); ok {
		start, end = time.UnixMilli(from), time.UnixMilli(to)
	}
	data, err := c.api.RenderSnapshot(ctx, assistant.SnapshotOptions{
		Definition: r.Definition, Start: start, End: end,
		Width: 820, Height: 450, Dark: r.Dark, // ddviz's usual size
	})
	if err != nil {
		return RenderResult{Status: remoteFailure(err)}
	}
	image, err := NewSnapshot(data, Availability{})
	if err != nil {
		return RenderResult{Status: Status{Phase: Failed, Reason: "Datadog returned an unreadable image.", Retry: RetryAllowed}}
	}
	return RenderResult{Status: Status{Phase: Succeeded}, Image: image}
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

// Open is never offered: Interactive reports false.
func (c *remoteClient) Open(context.Context, Request) Status {
	return Status{Phase: Unavailable}
}

func (c *remoteClient) Interactive() bool { return false }

func (c *remoteClient) Close() error {
	c.mu.Lock()
	c.cancel()
	c.mu.Unlock()
	c.work.Wait()
	return nil
}

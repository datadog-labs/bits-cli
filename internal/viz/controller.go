package viz

import "context"

// State describes the current request's independent render and open operations.
// Image is immutable and may be retained across updates.
type State struct {
	Render Status
	Open   Status
	Image  *Snapshot
	closed bool
}

func (s State) CanRender() bool {
	return !s.closed && canStart(s.Render, false)
}

func (s State) CanOpen() bool {
	return !s.closed && canStart(s.Open, true)
}

func canStart(status Status, repeat bool) bool {
	switch status.Phase {
	case Idle:
		return true
	case Succeeded:
		return repeat
	case Failed, Canceled:
		return status.Retry == RetryAllowed
	case Loading, Unavailable:
		return false
	}
	return false
}

// Task runs once outside the event loop and returns a completion to apply on it.
type Task func() Completion

// Completion is opaque and may safely be delivered to any controller.
type Completion struct {
	owner  *Controller
	id     uint64
	status Status
	image  *Snapshot
}

// Controller methods belong to the consumer's event loop. Tasks never mutate
// controller state. Close cancels pending work without closing the shared Client.
type Controller struct {
	client       Client
	request      Request
	sequence     uint64
	renderID     uint64
	openID       uint64
	renderCancel context.CancelFunc
	openCancel   context.CancelFunc
	state        State
}

// NewController retains one widget revision read-only.
func NewController(client Client, request Request) *Controller {
	return &Controller{client: client, request: request}
}

// Render starts a preview or permitted retry without disturbing Open.
func (c *Controller) Render(ctx context.Context) Task {
	if !c.state.CanRender() {
		return nil
	}
	c.sequence++
	c.renderID = c.sequence
	c.state.Render = Status{Phase: Loading}

	ctx, cancel := context.WithCancel(ctx)
	c.renderCancel = cancel
	client, request, id := c.client, c.request, c.renderID

	return func() Completion {
		defer cancel()
		result := client.Render(ctx, request)
		return Completion{owner: c, id: id, status: result.Status, image: result.Image}
	}
}

// Open presents the widget independently of preview rendering.
func (c *Controller) Open(ctx context.Context) Task {
	if !c.state.CanOpen() {
		return nil
	}
	c.sequence++
	c.openID = c.sequence
	c.state.Open = Status{Phase: Loading}

	ctx, cancel := context.WithCancel(ctx)
	c.openCancel = cancel
	client, request, id := c.client, c.request, c.openID

	return func() Completion {
		defer cancel()
		return Completion{owner: c, id: id, status: client.Open(ctx, request)}
	}
}

// Apply accepts each current completion once and ignores foreign or stale work.
func (c *Controller) Apply(done Completion) bool {
	if c.state.closed || done.owner != c || done.id == 0 {
		return false
	}

	switch done.id {
	case c.renderID:
		c.renderCancel()
		c.renderCancel = nil
		c.renderID = 0
		c.state.Render = done.status
		if done.image != nil {
			c.state.Image = done.image
		}

		if done.image != nil && !done.image.Interactive().Available && c.state.Open.Phase == Idle {
			c.state.Open = Status{Phase: Unavailable, Reason: done.image.Interactive().Reason}
		}
	case c.openID:
		c.openCancel()
		c.openCancel = nil
		c.openID = 0
		c.state.Open = done.status
	default:
		return false
	}
	return true
}

// State returns the current statuses and a shared immutable image.
func (c *Controller) State() State {
	return c.state
}

func (c *Controller) cancel() {
	if c.renderCancel != nil {
		c.renderCancel()
		c.renderCancel = nil
	}
	if c.openCancel != nil {
		c.openCancel()
		c.openCancel = nil
	}
	c.renderID = 0
	c.openID = 0
}

func (c *Controller) Close() {
	c.cancel()
	c.state.closed = true
	if c.state.Render.Phase == Loading {
		c.state.Render = Status{Phase: Canceled}
	}
	if c.state.Open.Phase == Loading {
		c.state.Open = Status{Phase: Canceled}
	}
}

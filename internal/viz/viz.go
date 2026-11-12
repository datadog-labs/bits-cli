// Package viz renders Datadog MCP apps without exposing viewer or auth details.
package viz

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"image/png"
	"slices"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// DatavizURI is the only app resource ddviz hosts.
const DatavizURI = "ui://apps/dataviz"

// Request borrows App read-only, including its raw result.
type Request struct {
	App assistant.AppPayload
	// ConversationID and MessageID locate a saved app for RestoreApp.
	ConversationID, MessageID string
	Dark                      bool // for snapshots, matching the terminal; ddviz follows macOS
}

func (r Request) valid() bool {
	if r.App.ResourceURI != DatavizURI {
		return false
	}
	return len(r.App.Result) > 0 || r.App.Restorable && r.ConversationID != "" && r.MessageID != ""
}

// Phase includes transient controller states and terminal client outcomes.
type Phase uint8

const (
	Idle Phase = iota
	Loading
	Succeeded
	Unavailable
	Failed
	Canceled
)

type RetryAdvice uint8

const (
	RetryNotUseful RetryAdvice = iota
	RetryAllowed
)

type Status struct {
	Phase  Phase
	Reason string
	Retry  RetryAdvice
}

type Availability struct {
	Available bool
	Reason    string
}

// Result is the outcome of an operation. App is set when the operation
// restored the app, to reuse until its relay session expires.
type Result struct {
	Status Status
	Image  *Snapshot
	App    *assistant.AppPayload
}

// Client is safe for concurrent use. Implementations own their resources, but
// not the injected API. Close cancels and joins viewer work.
// Render and Open return only terminal statuses, never Idle or Loading.
type Client interface {
	Render(context.Context, Request) Result
	// Open succeeds when presentation is requested, without waiting for panel
	// closure. Without a panel it reports Unavailable, with the app it restored.
	Open(context.Context, Request) Result
	// Interactive reports, before any operation, whether Open can present a panel.
	Interactive() bool
	Close() error
}

// Snapshot is an immutable PNG and its presentation metadata.
// Consumers must treat the encoded bytes as read-only.
type Snapshot struct {
	png         []byte
	width       int
	height      int
	interactive Availability
}

// PNG returns the encoded image without copying. Callers must not modify it.
func (s *Snapshot) PNG() []byte {
	return s.png
}

func (s *Snapshot) Width() int                { return s.width }
func (s *Snapshot) Height() int               { return s.height }
func (s *Snapshot) Interactive() Availability { return s.interactive }

// NewSnapshot validates and copies a PNG so the snapshot can be safely shared.
func NewSnapshot(data []byte, interactive Availability) (*Snapshot, error) {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width > 8192 || config.Height > 8192 || config.Width*config.Height > 16<<20 {
		return nil, errors.New("snapshot dimensions exceed limit")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return nil, err
	}

	return &Snapshot{
		png:         append([]byte(nil), data...),
		width:       config.Width,
		height:      config.Height,
		interactive: interactive,
	}, nil
}

// restorer reopens saved apps; *assistant.Client implements it.
type restorer interface {
	RestoreApp(ctx context.Context, conversationID, messageID string) (assistant.AppPayload, error)
}

// load returns the app to present, restoring a saved one first. The restored
// app is also returned on its own, for the caller to keep.
func load(ctx context.Context, api restorer, r Request) (assistant.AppPayload, *assistant.AppPayload, Status) {
	if !r.valid() {
		return assistant.AppPayload{}, nil, Status{Phase: Failed, Reason: "Request is invalid.", Retry: RetryNotUseful}
	}
	if len(r.App.Result) > 0 {
		return r.App, nil, Status{Phase: Succeeded}
	}
	app, err := api.RestoreApp(ctx, r.ConversationID, r.MessageID)
	if err != nil {
		return assistant.AppPayload{}, nil, restoreFailure(err)
	}
	if !(Request{App: app}).valid() {
		return assistant.AppPayload{}, nil, Status{Phase: Failed, Reason: "Datadog did not return a renderable view.", Retry: RetryNotUseful}
	}
	return app, &app, Status{Phase: Succeeded}
}

// restoreFailure keeps the server's reason when it refuses: the view is gone,
// or this login may no longer see its data.
func restoreFailure(err error) Status {
	refused := []error{assistant.ErrForbidden, assistant.ErrNotFound, assistant.ErrConflict, assistant.ErrGone, assistant.ErrBadRequest}
	var api *assistant.APIError
	if errors.As(err, &api) && slices.ContainsFunc(refused, func(e error) bool { return errors.Is(err, e) }) {
		return Status{Phase: Failed, Reason: cmp.Or(api.Detail, api.Title, "This view can no longer be opened."), Retry: RetryNotUseful}
	}
	return failure(err)
}

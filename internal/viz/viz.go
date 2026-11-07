// Package viz renders Datadog widgets without exposing viewer or auth details.
package viz

import (
	"bytes"
	"context"
	"errors"
	"image/png"

	"github.com/datadog-labs/bits-cli/internal/assistant"
)

// Request borrows Definition read-only, including its nested maps and slices.
type Request struct {
	Definition assistant.WidgetDefinition
	Dark       bool // for snapshots, matching the terminal; ddviz follows macOS
}

func (r Request) valid() bool {
	kind, _ := r.Definition["type"].(string)
	return kind != ""
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

type RenderResult struct {
	Status Status
	Image  *Snapshot
}

// Client is safe for concurrent use. Implementations own their resources, but
// not the injected MCP client. Close cancels and joins viewer work.
// Render and Open return only terminal statuses, never Idle or Loading.
type Client interface {
	Render(context.Context, Request) RenderResult
	// Open succeeds when presentation is requested, without waiting for panel closure.
	Open(context.Context, Request) Status
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
